// Package relay is a stateless ciphertext forwarder. A daemon ("host") and an app
// ("client") both dial the relay outbound and are bridged by server_id; the relay
// copies opaque messages between them without opening inbound ports on either.
//
// Security, stated as what the relay CAN and CANNOT do. An earlier version of this comment said the
// relay "cannot MITM", which is true only of confidentiality and was read here as a guarantee about
// the connection as a whole — a comment that overstates a property is worse than no comment,
// because it stops the next reader checking. The scheme underneath is static-static X25519 ECDH ->
// HKDF-SHA256 -> ChaCha20-Poly1305 (daemon/crypto/crypto.go). There is no Noise handshake anywhere
// in this system, no ephemeral keys, and no transcript binding.
//
// The relay CANNOT:
//   - read session content or the pairing secret. The channel key comes from ECDH between the
//     client and daemon key pairs; the relay sees only the two PUBLIC keys, and the pairing secret
//     travels as a sealed frame, never in the clear.
//   - substitute its own key and read the traffic. The client derives the channel from the daemon
//     public key it pinned off the pairing QR, so a swapped key produces a channel neither side can
//     open and the connection fails closed.
//
// The relay CAN:
//   - kill the connection at any time, and — unless the daemon registered with a proof of
//     possession (pop.go) — take the daemon's place at the relay. Encryption protects content, not
//     availability, and the server_id that grants the host slot is a PUBLIC value.
//   - record and replay. The transport contributes no server randomness and re-derives the same
//     static keys every session, so a recorded client->daemon stream re-authenticates and
//     re-executes against the real daemon (docs/security-interception-review.md §4.3). Recording is
//     exactly what a bridge is positioned to do.
//   - see who talks to whom, when, and how much: both endpoints' IPs, connection lifetimes, frame
//     sizes and timing, and the ?sid= — the daemon's permanent public key — on every request, in
//     request logs included.
//
// And there is no forward secrecy to fall back on: the daemon's static key is long-lived, so anyone
// who later reads ~/.oculus/key decrypts every session they recorded. An ephemeral handshake is the
// tracked follow-up noted in daemon/crypto/crypto.go:13.
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
	popTimeout time.Duration // proof-of-possession read bound (per-instance for the same reason)
}

// hostEntry is a registered host awaiting (or serving) a client. evict is closed
// to unblock an idle host's select when it is superseded by a newer registration
// for the same server_id.
//
// proven records that this host answered the challenge in pop.go, i.e. that it holds the private
// key behind the server_id rather than merely knowing the public one. It is the only thing
// separating the real daemon from anyone who has seen a pairing QR, so it is what claimHost
// arbitrates on.
type hostEntry struct {
	pair   chan *pairing
	ws     *websocket.Conn
	evict  chan struct{}
	proven bool
}

type pairing struct {
	clientWS *websocket.Conn
	done     chan struct{}
}

// New returns an empty Relay.
func New() *Relay {
	return &Relay{hosts: map[string]*hostEntry{}, regTimeout: defaultRegistrationTimeout, popTimeout: defaultPopTimeout}
}

// Handler is the relay's WebSocket endpoint. Registration is by URL query (?sid=&role=) — the
// unified protocol shared with the Cloudflare Durable-Object relay, which lets an edge route to the
// right instance without reading a frame. A first-frame JSON registration is still accepted as a
// fallback so already-deployed clients keep working.
func (r *Relay) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sid := req.URL.Query().Get("sid")
		role := req.URL.Query().Get("role")
		// ?pop=1 is a host announcing it will answer a proof-of-possession challenge (pop.go).
		// Absent — which is every daemon in the field today, and every legacy first-frame
		// registration below — the host registers unproven and nothing changes for it.
		pop := req.URL.Query().Get(popQuery) == "1"

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
			r.serveHost(ctx, sid, ws, pop)
		case roleClient:
			r.serveClient(ctx, sid, ws)
		default:
			ws.Close(websocket.StatusPolicyViolation, "bad role")
		}
	})
}

func (r *Relay) serveHost(ctx context.Context, id string, hostWS *websocket.Conn, pop bool) {
	proven := false
	if pop {
		// Run the challenge BEFORE claiming the slot: the answer is what decides the claim, so it
		// cannot be deferred to after the incumbent has already been evicted.
		if err := verifyHostPossession(ctx, hostWS, id, r.popTimeout); err != nil {
			hostWS.Close(websocket.StatusPolicyViolation, "host proof failed")
			return
		}
		proven = true
	}

	entry := &hostEntry{
		pair:   make(chan *pairing, 1),
		ws:     hostWS,
		evict:  make(chan struct{}),
		proven: proven,
	}
	old, ok := r.claimHost(id, entry)
	if !ok {
		// A host that PROVED it holds this server_id's private key is already registered and this
		// one did not prove anything. Refusing is the hijack fix: the newcomer knows only the
		// public key — printed on daemon start, embedded in every pairing QR, and in the relay's
		// own logs — so letting it evict the incumbent would hand a permanent remote denial of
		// service, plus a ciphertext-recording position, to anyone who has ever seen that value.
		hostWS.Close(websocket.StatusPolicyViolation, "host slot held by a proven host")
		return
	}
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

// claimHost installs entry as the host for id, returning the entry it displaced (for the caller to
// evict outside the lock) and whether the claim was allowed at all.
//
// Newest-wins stays the default, deliberately: the overwhelmingly common conflict is the daemon
// itself re-dialing after a half-open connection, and refusing the newcomer there would pin the
// user's remote access to a corpse until the OS noticed the dead TCP connection — minutes at best,
// and self-inflicted. The single exception is the hijack fix: a host that proved possession of the
// server_id's private key may not be evicted by one that has not.
//
// Note what that exception does NOT cover, so nobody reads more into it than it gives: two UNPROVEN
// hosts still race, newest wins. An "incumbent always wins" rule without the proof would not have
// closed the denial of service, only reshaped it — an attacker who registers first would then hold
// the slot against the real daemon indefinitely, which is strictly worse than being evicted and
// re-registering. Knowing the incumbent is legitimate is exactly what the proof buys.
func (r *Relay) claimHost(id string, entry *hostEntry) (*hostEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.hosts[id]; old != nil {
		if old.proven && !entry.proven {
			return nil, false
		}
		r.hosts[id] = entry
		return old, true
	}
	r.hosts[id] = entry
	return nil, true
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
//
// It registers UNPROVEN: any peer that knows serverID — a public value — can evict it from the
// relay's host slot. Prefer ServeHostKey, which does not have that property.
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
	return ServeHostKey(ctx, relayURL, serverID, nil, ka, serveConn)
}

// ServeHostKey is ServeHostKeepalive that also PROVES the registration, by answering the relay's
// proof-of-possession challenge with hostPriv (the daemon's 32-byte X25519 private key, whose
// public half is serverID). See pop.go for why that matters: without it, the relay's host slot is
// granted on presentation of a value that is printed to stdout, embedded in every pairing QR, and
// logged by the relay, so anyone who has seen it can evict the daemon and take the bridge position.
//
// hostPriv that does not match serverID, or a relay that has not been updated, both degrade
// silently to an unproven registration rather than failing — see popCanProve and popConn.
func ServeHostKey(ctx context.Context, relayURL, serverID string, hostPriv []byte, ka KeepaliveConfig, serveConn func(context.Context, transport.MsgConn) error) error {
	mc, err := dialHost(ctx, relayURL, serverID, hostPriv)
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
	return dialRegister(ctx, relayURL, roleClient, serverID, false)
}

// hostConn is a registered host socket: a MsgConn for serveConn plus the liveness prober.
type hostConn interface {
	transport.MsgConn
	Keepalive(interval, timeout time.Duration) func()
}

func dialHost(ctx context.Context, relayURL, serverID string, hostPriv []byte) (hostConn, error) {
	sid, canProve := popCanProve(serverID, hostPriv)
	u, err := registerURL(relayURL, roleHost, serverID, canProve)
	if err != nil {
		return nil, err
	}
	if !canProve {
		mc, err := wsmsg.Dial(ctx, u)
		if err != nil {
			return nil, err
		}
		return mc, nil
	}
	// Dial the socket directly rather than through wsmsg.Dial: popConn needs the frame TYPE to tell
	// a relay control frame from session ciphertext, and MsgConn deliberately does not carry it.
	ws, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	return &popConn{Conn: wsmsg.New(ctx, ws), ws: ws, ctx: ctx, sid: sid, hostPriv: hostPriv}, nil
}

func dialRegister(ctx context.Context, rawURL, role, serverID string, pop bool) (*wsmsg.Conn, error) {
	u, err := registerURL(rawURL, role, serverID, pop)
	if err != nil {
		return nil, err
	}
	return wsmsg.Dial(ctx, u)
}

// registerURL builds the registration URL. Registration is by URL query (?sid=&role=) — the unified
// protocol shared with the Cloudflare relay; no first frame, so the relay/edge routes before
// touching the socket.
func registerURL(rawURL, role, serverID string, pop bool) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("sid", serverID)
	q.Set("role", role)
	if pop {
		q.Set(popQuery, "1")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// popConn answers the relay's proof-of-possession challenge from inside the serve loop's read,
// rather than as a handshake step after dialling.
//
// WHY this way round: an older relay — the Fly deployment before it is rebuilt, or the Cloudflare
// Worker before `wrangler deploy` — ignores ?pop=1 and never sends a challenge. A daemon that
// blocked waiting for one would time out on every dial and lose remote access entirely against a
// relay that works fine. Intercepting instead means the daemon never waits for anything: if a
// challenge arrives it is answered and swallowed before it can reach ServerHandshake (which would
// otherwise take it for a malformed client_hello and drop the connection), and if none arrives the
// connection behaves exactly as it always did.
//
// Only TEXT frames that parse as a pop message are intercepted. Session traffic is binary, and any
// other text frame is passed through untouched, so this cannot swallow a peer's data.
type popConn struct {
	*wsmsg.Conn
	ws       *websocket.Conn
	ctx      context.Context
	sid      []byte
	hostPriv []byte

	// answered bounds the exchange to once per connection. Nothing is leaked by answering a second
	// challenge (each MAC is under a fresh ephemeral shared secret and the daemon's private key
	// stays put), but a relay has no reason to ask twice, and one answer is one answer's worth of
	// surface. Only ever touched by the single serve-loop reader, so no lock.
	answered bool
}

func (c *popConn) ReadMsg() ([]byte, error) {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageText {
			return data, nil
		}
		var msg popMsg
		if json.Unmarshal(data, &msg) != nil || msg.IR != popChallenge {
			return data, nil
		}
		if c.answered {
			continue
		}
		c.answered = true
		proof, err := answerHostChallenge(msg, c.sid, c.hostPriv)
		if err != nil {
			// We opted in and cannot answer: the relay is about to close this socket, so end the
			// read now and let the caller's re-dial loop run rather than waiting for it.
			return nil, err
		}
		wctx, cancel := context.WithTimeout(c.ctx, defaultPopTimeout)
		err = c.ws.Write(wctx, websocket.MessageText, proof)
		cancel()
		if err != nil {
			return nil, err
		}
	}
}
