package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/curve25519"
)

// --- helpers ---

func newHostKey(t *testing.T) (priv []byte, sid string) {
	t.Helper()
	priv = make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, hex.EncodeToString(pub)
}

func relayServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(srv.CloseClientConnections)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// dialHostSocket registers as a host by URL query, exactly as a daemon (or an attacker who read the
// server_id off a pairing QR) does.
func dialHostSocket(t *testing.T, ctx context.Context, relayURL, sid string, pop bool) *websocket.Conn {
	t.Helper()
	u, err := registerURL(relayURL, roleHost, sid, pop)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// provenHost registers a host and answers the relay's challenge, i.e. what an updated daemon does.
func provenHost(t *testing.T, ctx context.Context, relayURL, sid string, priv []byte) *websocket.Conn {
	t.Helper()
	ws := dialHostSocket(t, ctx, relayURL, sid, true)
	typ, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("challenge frame type = %v, want text", typ)
	}
	var msg popMsg
	if err := json.Unmarshal(data, &msg); err != nil || msg.IR != popChallenge {
		t.Fatalf("challenge = %s (unmarshal err %v)", data, err)
	}
	sidBytes, err := hex.DecodeString(sid)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := answerHostChallenge(msg, sidBytes, priv)
	if err != nil {
		t.Fatalf("answer challenge: %v", err)
	}
	if err := ws.Write(ctx, websocket.MessageText, proof); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	settle()
	return ws
}

// settle lets the relay finish claiming a slot before the next registration races it. The relay
// acts on the proof after the writer's Write returns, so without this the assertions would be
// testing goroutine scheduling rather than the slot rules.
func settle() { time.Sleep(200 * time.Millisecond) }

// bridgeWorks proves the given host socket is the one a client actually reaches.
func bridgeWorks(t *testing.T, ctx context.Context, relayURL, sid string, host *websocket.Conn) {
	t.Helper()
	u, err := registerURL(relayURL, roleClient, sid, false)
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.CloseNow()
	if err := client.Write(ctx, websocket.MessageBinary, []byte("c2h")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, got, err := host.Read(rctx)
	if err != nil {
		t.Fatalf("host did not receive the client's frame: %v", err)
	}
	if string(got) != "c2h" {
		t.Fatalf("host received %q, want %q", got, "c2h")
	}
}

func wantClosed(t *testing.T, ctx context.Context, ws *websocket.Conn, why string) {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _, err := ws.Read(rctx)
	if err == nil {
		t.Fatalf("%s: socket still open", why)
	}
	if code := websocket.CloseStatus(err); code != websocket.StatusPolicyViolation {
		t.Fatalf("%s: close status = %v (err %v), want %v", why, code, err, websocket.StatusPolicyViolation)
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- the hijack ---

// TestProvenHostCannotBeEvictedByPublicKeyAlone is the fix for the host-slot hijack
// (docs/security-interception-review.md §4.2). The server_id is the daemon's PUBLIC key — printed
// on daemon start, in every pairing QR, in the relay's logs — so before this, presenting it was
// enough to evict the real daemon and become the bridge. Now it is not.
func TestProvenHostCannotBeEvictedByPublicKeyAlone(t *testing.T) {
	ctx := testCtx(t)
	relayURL := relayServer(t)
	priv, sid := newHostKey(t)

	daemon := provenHost(t, ctx, relayURL, sid, priv)

	// The attacker has the server_id and nothing else — the whole point of the finding.
	attacker := dialHostSocket(t, ctx, relayURL, sid, false)
	wantClosed(t, ctx, attacker, "unproven host claiming a proven slot")

	// And the daemon is still the bridge, not merely still connected.
	bridgeWorks(t, ctx, relayURL, sid, daemon)
}

// TestProvenHostReclaimsItsOwnSlot: the protection must not lock the daemon out of its own slot.
// Its re-dial loop registers a fresh socket every time a client disconnects, and each of those is
// proven, so newest-wins still applies between them.
func TestProvenHostReclaimsItsOwnSlot(t *testing.T) {
	ctx := testCtx(t)
	relayURL := relayServer(t)
	priv, sid := newHostKey(t)

	stale := provenHost(t, ctx, relayURL, sid, priv)
	fresh := provenHost(t, ctx, relayURL, sid, priv)

	wantClosed(t, ctx, stale, "superseded proven host")
	bridgeWorks(t, ctx, relayURL, sid, fresh)
}

// TestProvenHostEvictsUnprovenSquatter: an attacker who got there first while the daemon was
// offline must not be able to hold the slot. This is why the rule is "proven beats unproven" and
// not "the incumbent always wins" — the latter would have turned an eviction loop into a permanent
// squat, which is worse.
func TestProvenHostEvictsUnprovenSquatter(t *testing.T) {
	ctx := testCtx(t)
	relayURL := relayServer(t)
	priv, sid := newHostKey(t)

	squatter := dialHostSocket(t, ctx, relayURL, sid, false)
	settle()

	daemon := provenHost(t, ctx, relayURL, sid, priv)
	wantClosed(t, ctx, squatter, "squatter displaced by the real daemon")
	bridgeWorks(t, ctx, relayURL, sid, daemon)
}

// TestBadProofIsRefusedAndLeavesTheIncumbent: opting in with a wrong answer must not be a way to
// evict the incumbent as a side effect of being refused.
func TestBadProofIsRefusedAndLeavesTheIncumbent(t *testing.T) {
	ctx := testCtx(t)
	relayURL := relayServer(t)
	priv, sid := newHostKey(t)

	daemon := provenHost(t, ctx, relayURL, sid, priv)

	// An attacker who knows the protocol but not the key: answer the challenge with noise.
	attacker := dialHostSocket(t, ctx, relayURL, sid, true)
	if _, _, err := attacker.Read(ctx); err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	forged, _ := json.Marshal(popMsg{IR: popProof, V: 1, MAC: strings.Repeat("aa", 32)})
	if err := attacker.Write(ctx, websocket.MessageText, forged); err != nil {
		t.Fatalf("write forged proof: %v", err)
	}
	wantClosed(t, ctx, attacker, "forged proof")

	bridgeWorks(t, ctx, relayURL, sid, daemon)
}

// TestPopRequiresAPublicKeySID: ?pop=1 on a server_id that is not a 32-byte hex public key can
// never be answered, so it is refused at once rather than parked for the read deadline.
func TestPopRequiresAPublicKeySID(t *testing.T) {
	ctx := testCtx(t)
	relayURL := relayServer(t)
	ws := dialHostSocket(t, ctx, relayURL, "srv-1", true)
	wantClosed(t, ctx, ws, "?pop=1 with a symbolic server_id")
}

// TestUnprovenHostsStillNewestWins pins the compatibility behaviour: two daemons that predate the
// proof race exactly as they always did. Changing this would strand a real daemon behind a stale
// registration it can never displace.
func TestUnprovenHostsStillNewestWins(t *testing.T) {
	ctx := testCtx(t)
	relayURL := relayServer(t)
	_, sid := newHostKey(t)

	stale := dialHostSocket(t, ctx, relayURL, sid, false)
	settle()
	fresh := dialHostSocket(t, ctx, relayURL, sid, false)
	settle()

	wantClosed(t, ctx, stale, "superseded unproven host")
	bridgeWorks(t, ctx, relayURL, sid, fresh)
}

// --- the daemon half ---

// TestPopConnAnswersAChallengeWithoutSurfacingIt: the challenge must never reach ServerHandshake,
// which would take it for a malformed client_hello and drop the connection.
func TestPopConnAnswersAChallengeWithoutSurfacingIt(t *testing.T) {
	ctx := testCtx(t)
	priv, sid := newHostKey(t)

	proofs := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		if err := verifyHostPossession(req.Context(), ws, sid, 5*time.Second); err != nil {
			proofs <- nil
			return
		}
		proofs <- []byte("verified")
		// Then behave like a bridge: forward one session frame.
		_ = ws.Write(req.Context(), websocket.MessageBinary, []byte("client_hello"))
		<-req.Context().Done()
	}))
	defer srv.Close()

	mc, err := dialHost(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), sid, priv)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	defer mc.Close()
	if _, ok := mc.(*popConn); !ok {
		t.Fatalf("dialHost returned %T, want *popConn when the key matches the server_id", mc)
	}

	got, err := mc.ReadMsg()
	if err != nil {
		t.Fatalf("ReadMsg: %v", err)
	}
	if string(got) != "client_hello" {
		t.Fatalf("ReadMsg surfaced %q; the pop challenge must be swallowed, not delivered", got)
	}
	select {
	case v := <-proofs:
		if v == nil {
			t.Fatal("relay rejected the daemon's proof")
		}
	default:
		t.Fatal("relay never verified a proof")
	}
}

// TestPopConnPassesThroughWhenTheRelayNeverChallenges is the compatibility direction that matters
// most operationally: a daemon that has been updated must keep working against a relay that has
// NOT been redeployed. An old relay ignores ?pop=1 and sends no challenge, so the daemon must not
// wait for one.
func TestPopConnPassesThroughWhenTheRelayNeverChallenges(t *testing.T) {
	ctx := testCtx(t)
	priv, sid := newHostKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		// An old relay: no challenge, straight to bridging.
		_ = ws.Write(req.Context(), websocket.MessageBinary, []byte("client_hello"))
		<-req.Context().Done()
	}))
	defer srv.Close()

	mc, err := dialHost(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), sid, priv)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	defer mc.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := mc.ReadMsg()
		if err != nil {
			t.Errorf("ReadMsg against a relay that never challenges: %v", err)
			return
		}
		if string(got) != "client_hello" {
			t.Errorf("ReadMsg = %q, want the session frame passed straight through", got)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon blocked waiting for a challenge that an un-redeployed relay never sends")
	}
}

// TestPopConnPassesThroughForeignTextFrames: only frames that parse as a pop challenge may be
// intercepted, or the interceptor would silently eat a peer's data.
func TestPopConnPassesThroughForeignTextFrames(t *testing.T) {
	ctx := testCtx(t)
	priv, sid := newHostKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_ = ws.Write(req.Context(), websocket.MessageText, []byte(`{"ir":"something-else"}`))
		<-req.Context().Done()
	}))
	defer srv.Close()

	mc, err := dialHost(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), sid, priv)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	defer mc.Close()

	got, err := mc.ReadMsg()
	if err != nil {
		t.Fatalf("ReadMsg: %v", err)
	}
	if string(got) != `{"ir":"something-else"}` {
		t.Fatalf("ReadMsg = %q, want the foreign text frame passed through", got)
	}
}

// TestPopCanProveRefusesAMismatchedKey: opting in with a key that does not match the server_id
// would fail every challenge and take remote access down. Degrading to an unproven registration is
// the failure mode worth having.
func TestPopCanProveRefusesAMismatchedKey(t *testing.T) {
	priv, sid := newHostKey(t)
	other, otherSID := newHostKey(t)

	if _, ok := popCanProve(sid, priv); !ok {
		t.Fatal("a key that matches its server_id must be able to prove")
	}
	if _, ok := popCanProve(otherSID, priv); ok {
		t.Fatal("a key that does not match the server_id must not opt in")
	}
	if _, ok := popCanProve("srv-1", other); ok {
		t.Fatal("a symbolic server_id must not opt in")
	}
	if _, ok := popCanProve(sid, nil); ok {
		t.Fatal("no key must not opt in")
	}
}

// TestPopMACBindsToTheChallengeAndTheSlot: a proof recorded off the wire must be worthless against
// the next challenge, and against a different server_id.
func TestPopMACBindsToTheChallengeAndTheSlot(t *testing.T) {
	shared := []byte("shared-secret-shared-secret-32by")
	nonce := []byte("nonce-a")
	sid := []byte("sid-a")

	base := hex.EncodeToString(popMAC(shared, nonce, sid))
	if hex.EncodeToString(popMAC(shared, []byte("nonce-b"), sid)) == base {
		t.Fatal("MAC does not depend on the nonce: a recorded proof would replay")
	}
	if hex.EncodeToString(popMAC(shared, nonce, []byte("sid-b"))) == base {
		t.Fatal("MAC does not depend on the server_id: a proof would transfer between slots")
	}
	if hex.EncodeToString(popMAC([]byte("different-secret-different-secre"), nonce, sid)) == base {
		t.Fatal("MAC does not depend on the shared secret")
	}
}
