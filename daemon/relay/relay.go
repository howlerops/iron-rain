// Package relay is a stateless ciphertext forwarder. A daemon ("host") and an app
// ("client") both dial the relay outbound and are bridged by server_id; the relay
// copies opaque messages between them without opening inbound ports on either.
//
// Security note (v0): the relay forwards the transport handshake, so the pairing
// secret + public keys transit the relay in cleartext (session CONTENT stays E2E
// encrypted — the relay cannot read it). Hardening (prove secret knowledge without
// revealing it) is a tracked follow-up.
package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/wsmsg"
)

const (
	roleHost   = "host"
	roleClient = "client"

	// defaultRegistrationTimeout bounds the first (registration) read on a freshly
	// accepted relay socket. Without it a peer that opens the WebSocket and then sends
	// nothing would park a goroutine and hold the socket open for the life of the
	// connection — a cheap slowloris vector on a public relay.
	defaultRegistrationTimeout = 10 * time.Second
)

type registration struct {
	Role     string `json:"role"`
	ServerID string `json:"server_id"`
}

// Relay bridges hosts and clients by server_id.
type Relay struct {
	mu         sync.Mutex
	hosts      map[string]*hostEntry
	regTimeout time.Duration // registration-phase read bound (per-instance so tests can shorten it)
}

// hostEntry is a registered host awaiting (or serving) a client. evict is closed
// to unblock an idle host's select when it is superseded by a newer registration
// for the same server_id.
type hostEntry struct {
	pair  chan *pairing
	ws    *websocket.Conn
	evict chan struct{}
}

type pairing struct {
	clientWS *websocket.Conn
	done     chan struct{}
}

// New returns an empty Relay.
func New() *Relay {
	return &Relay{hosts: map[string]*hostEntry{}, regTimeout: defaultRegistrationTimeout}
}

// Handler is the relay's WebSocket endpoint.
func (r *Relay) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ws.SetReadLimit(8 * 1024 * 1024)
		ctx := req.Context()

		// Bound only the registration phase; switch to the unbounded connection
		// context after a valid registration completes.
		rctx, cancel := context.WithTimeout(ctx, r.regTimeout)
		_, data, err := ws.Read(rctx)
		cancel()
		if err != nil {
			ws.Close(websocket.StatusPolicyViolation, "no registration")
			return
		}
		var reg registration
		if json.Unmarshal(data, &reg) != nil || reg.ServerID == "" {
			ws.Close(websocket.StatusPolicyViolation, "bad registration")
			return
		}

		switch reg.Role {
		case roleHost:
			r.serveHost(ctx, reg.ServerID, ws)
		case roleClient:
			r.serveClient(ctx, reg.ServerID, ws)
		default:
			ws.Close(websocket.StatusPolicyViolation, "bad role")
		}
	})
}

func (r *Relay) serveHost(ctx context.Context, id string, hostWS *websocket.Conn) {
	entry := &hostEntry{
		pair:  make(chan *pairing, 1),
		ws:    hostWS,
		evict: make(chan struct{}),
	}
	r.mu.Lock()
	old := r.hosts[id]
	r.hosts[id] = entry
	r.mu.Unlock()
	if old != nil {
		// A previous host is still registered for this id (a reconnect after a
		// half-open connection, or an id collision). Evict it so there is always
		// exactly one live host per server_id and the old goroutine can't strand
		// on a channel no client can reach. Closing its ws also tears down an
		// old bridge if it had already paired; closing evict unblocks it if it
		// was still idle in its select.
		old.ws.Close(websocket.StatusPolicyViolation, "replaced by newer host registration")
		close(old.evict)
	}
	defer func() {
		r.mu.Lock()
		if r.hosts[id] == entry {
			delete(r.hosts, id)
		}
		r.mu.Unlock()
	}()

	select {
	case p := <-entry.pair:
		bridge(ctx, hostWS, p.clientWS)
		close(p.done)
	case <-entry.evict:
		// Superseded by a newer registration; the evictor already closed our ws.
	case <-ctx.Done():
		hostWS.Close(websocket.StatusNormalClosure, "")
	}
}

func (r *Relay) serveClient(ctx context.Context, id string, clientWS *websocket.Conn) {
	r.mu.Lock()
	entry := r.hosts[id]
	r.mu.Unlock()
	if entry == nil {
		clientWS.Close(websocket.StatusPolicyViolation, "no host for server_id")
		return
	}
	done := make(chan struct{})
	select {
	case entry.pair <- &pairing{clientWS: clientWS, done: done}:
		<-done
	case <-ctx.Done():
		clientWS.Close(websocket.StatusNormalClosure, "")
	}
}

func bridge(ctx context.Context, a, b *websocket.Conn) {
	errc := make(chan error, 2)
	go copyWS(ctx, a, b, errc)
	go copyWS(ctx, b, a, errc)
	<-errc
	a.Close(websocket.StatusNormalClosure, "")
	b.Close(websocket.StatusNormalClosure, "")
}

func copyWS(ctx context.Context, src, dst *websocket.Conn, errc chan<- error) {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			errc <- err
			return
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			errc <- err
			return
		}
	}
}

// --- host/client dial helpers (used by the daemon and app clients) ---

// ServeHost dials the relay, registers as the host for serverID, and serves the
// bridged client via serveConn (typically server.Server.ServeConn). Serves one
// client connection; call in a loop to accept clients sequentially.
func ServeHost(ctx context.Context, relayURL, serverID string, serveConn func(context.Context, transport.MsgConn) error) error {
	mc, err := dialRegister(ctx, relayURL, roleHost, serverID)
	if err != nil {
		return err
	}
	return serveConn(ctx, mc)
}

// DialClient dials the relay and registers as a client for serverID, returning a
// MsgConn ready for transport.ClientHandshake.
func DialClient(ctx context.Context, relayURL, serverID string) (transport.MsgConn, error) {
	return dialRegister(ctx, relayURL, roleClient, serverID)
}

func dialRegister(ctx context.Context, url, role, serverID string) (*wsmsg.Conn, error) {
	mc, err := wsmsg.Dial(ctx, url)
	if err != nil {
		return nil, err
	}
	reg, _ := json.Marshal(registration{Role: role, ServerID: serverID})
	if err := mc.WriteMsg(reg); err != nil {
		_ = mc.Close()
		return nil, err
	}
	return mc, nil
}
