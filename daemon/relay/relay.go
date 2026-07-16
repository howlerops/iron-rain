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

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/wsmsg"
)

const (
	roleHost   = "host"
	roleClient = "client"
)

type registration struct {
	Role     string `json:"role"`
	ServerID string `json:"server_id"`
}

// Relay bridges hosts and clients by server_id.
type Relay struct {
	mu    sync.Mutex
	hosts map[string]chan *pairing
}

type pairing struct {
	clientWS *websocket.Conn
	done     chan struct{}
}

// New returns an empty Relay.
func New() *Relay { return &Relay{hosts: map[string]chan *pairing{}} }

// Handler is the relay's WebSocket endpoint.
func (r *Relay) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ws.SetReadLimit(8 * 1024 * 1024)
		ctx := req.Context()

		_, data, err := ws.Read(ctx)
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
	ch := make(chan *pairing, 1)
	r.mu.Lock()
	r.hosts[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.hosts[id] == ch {
			delete(r.hosts, id)
		}
		r.mu.Unlock()
	}()

	select {
	case p := <-ch:
		bridge(ctx, hostWS, p.clientWS)
		close(p.done)
	case <-ctx.Done():
		hostWS.Close(websocket.StatusNormalClosure, "")
	}
}

func (r *Relay) serveClient(ctx context.Context, id string, clientWS *websocket.Conn) {
	r.mu.Lock()
	ch := r.hosts[id]
	r.mu.Unlock()
	if ch == nil {
		clientWS.Close(websocket.StatusPolicyViolation, "no host for server_id")
		return
	}
	done := make(chan struct{})
	select {
	case ch <- &pairing{clientWS: clientWS, done: done}:
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
