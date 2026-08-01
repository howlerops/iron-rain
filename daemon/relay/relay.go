// Package relay is a stateless ciphertext forwarder. A daemon ("host") and an app
// ("client") both dial the relay outbound and are bridged by server_id; the relay
// copies opaque messages between them without opening inbound ports on either.
//
// Security: the relay forwards only opaque, end-to-end-encrypted bytes. The channel key is derived
// from static-static X25519 ECDH between the client and daemon keypairs, so the relay — which sees
// only the two PUBLIC keys — cannot derive it, cannot read session content, cannot read the pairing
// secret (sent sealed, not in the clear), and cannot MITM (the client already has the real daemon
// pubkey from the pairing QR). The one caveat is no forward secrecy: static keys mean a future key
// compromise could decrypt recorded traffic — a Noise-style ephemeral handshake is a tracked
// follow-up. It's independent of the relay itself.
package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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

// Handler is the relay's WebSocket endpoint. Registration is by URL query (?sid=&role=) — the
// unified protocol shared with the Cloudflare Durable-Object relay, which lets an edge route to the
// right instance without reading a frame. A first-frame JSON registration is still accepted as a
// fallback so already-deployed clients keep working.
func (r *Relay) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sid := req.URL.Query().Get("sid")
		role := req.URL.Query().Get("role")

		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ws.SetReadLimit(8 * 1024 * 1024)
		ctx := req.Context()

		if sid == "" { // legacy client: read the JSON registration frame instead
			// Bound only the registration phase; switch to the unbounded connection
			// context after a valid registration completes.
			rctx, cancel := context.WithTimeout(ctx, r.regTimeout)
			_, data, rerr := ws.Read(rctx)
			cancel()
			if rerr != nil {
				ws.Close(websocket.StatusPolicyViolation, "no registration")
				return
			}
			var reg registration
			if json.Unmarshal(data, &reg) != nil || reg.ServerID == "" {
				ws.Close(websocket.StatusPolicyViolation, "bad registration")
				return
			}
			sid, role = reg.ServerID, reg.Role
		}

		switch role {
		case roleHost:
			r.serveHost(ctx, sid, ws)
		case roleClient:
			r.serveClient(ctx, sid, ws)
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

	// ONE reader for this socket's whole life, started NOW rather than at pairing.
	//
	// coder/websocket services control frames only inside a Read. A host parked here with nobody
	// reading it therefore cannot answer a ping — so the daemon's own keepalive times out against a
	// perfectly healthy relay, tears the connection down and re-registers, forever, with a window on
	// every cycle where a client asking for that daemon is told no host exists. Adding the keepalive
	// without this made the relay hostile to exactly the connections it was meant to protect.
	//
	// The reader cannot simply be cancelled at pairing (cancelling a coder/websocket read closes the
	// connection), so it keeps running and the bridge consumes its output instead of reading directly.
	frames := make(chan wsFrame, 32)
	readErr := make(chan error, 1)
	go func() {
		defer close(frames)
		for {
			typ, data, err := hostWS.Read(ctx)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- wsFrame{typ: typ, data: data}:
			default:
				// Nothing is paired yet, or the client is too slow. A host has nothing useful to say
				// before a client arrives, so dropping is right: buffering unbounded would let an
				// unpaired daemon grow the relay's memory without limit.
			}
		}
	}()

	select {
	case p := <-entry.pair:
		bridgeFromReader(ctx, frames, readErr, hostWS, p.clientWS)
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

// wsFrame is one message read off a socket, carried between the idle reader and the bridge.
type wsFrame struct {
	typ  websocket.MessageType
	data []byte
}

// bridgeFromReader wires an already-reading host socket to a freshly paired client. The host half is
// consumed from the channel the idle reader owns; the client half is read directly.
func bridgeFromReader(ctx context.Context, frames <-chan wsFrame, readErr <-chan error, host, client *websocket.Conn) {
	errc := make(chan error, 2)
	go func() {
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					errc <- <-readErr
					return
				}
				if err := client.Write(ctx, f.typ, f.data); err != nil {
					errc <- err
					return
				}
			case err := <-readErr:
				errc <- err
				return
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			}
		}
	}()
	go copyWS(ctx, client, host, errc)
	<-errc
	host.Close(websocket.StatusNormalClosure, "")
	client.Close(websocket.StatusNormalClosure, "")
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
	return ServeHostKeepalive(ctx, relayURL, serverID, DefaultKeepalive, serveConn)
}

// KeepaliveConfig tunes the liveness probe on a registered host socket. It is a parameter
// rather than a package global so tests can drive it in milliseconds without mutating
// shared state (the relay package's tests run concurrently with each other).
type KeepaliveConfig struct {
	Interval time.Duration // how often to ping
	Timeout  time.Duration // how long to wait for the pong before declaring the socket dead
}

// DefaultKeepalive is what the daemon ships with. 25s is comfortably under the idle
// timeouts of the middleboxes a relay connection crosses (Cloudflare's ~100s, most NATs'
// 60-120s) while still being cheap; 10s for the pong is generous for a round trip to an
// edge PoP and still bounds detection to ~35s worst case.
var DefaultKeepalive = KeepaliveConfig{Interval: 25 * time.Second, Timeout: 10 * time.Second}

// ServeHostKeepalive is ServeHost with an injectable keepalive.
//
// The keepalive is what makes the re-dial loop in main.go's relayHost actually run: before
// this, a half-open relay socket left serveConn parked in an unbounded read, so the daemon
// stayed registered-but-unreachable until the OS noticed the dead TCP connection — minutes
// at best. With a pong deadline, the read unblocks within Interval+Timeout, ServeHost
// returns, and the caller re-registers within seconds.
func ServeHostKeepalive(ctx context.Context, relayURL, serverID string, ka KeepaliveConfig, serveConn func(context.Context, transport.MsgConn) error) error {
	mc, err := dialRegister(ctx, relayURL, roleHost, serverID)
	if err != nil {
		return err
	}
	// Stop the prober when serveConn returns for any reason, so a serve loop that ends on
	// its own doesn't leave a goroutine pinging a dead socket.
	stop := mc.Keepalive(ka.Interval, ka.Timeout)
	defer stop()
	return serveConn(ctx, mc)
}

// DialClient dials the relay and registers as a client for serverID, returning a
// MsgConn ready for transport.ClientHandshake.
func DialClient(ctx context.Context, relayURL, serverID string) (transport.MsgConn, error) {
	return dialRegister(ctx, relayURL, roleClient, serverID)
}

func dialRegister(ctx context.Context, rawURL, role, serverID string) (*wsmsg.Conn, error) {
	// Register via URL query (?sid=&role=) — the unified protocol shared with the Cloudflare relay;
	// no first frame, so the relay/edge routes before touching the socket.
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("sid", serverID)
	q.Set("role", role)
	u.RawQuery = q.Encode()
	return wsmsg.Dial(ctx, u.String())
}
