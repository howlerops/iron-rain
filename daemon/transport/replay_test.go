package transport

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
)

// These tests are the reason the handshake changed. Every one of them is written so that
// it can only pass for the right reason: a replay test that "passes" because the
// connection died of something unrelated would be worse than no test, so the negative
// cases assert WHERE the rejection happened (authorize was never reached, the error is
// ErrReplay, the daemon's answer was a refusal) rather than merely that an error came back.

// captureSession runs one complete, legitimate session and returns everything the client
// put on the wire, in order — exactly what a passive listener on an untrusted LAN gets,
// since the direct route has no TLS.
func captureSession(t *testing.T, daemonKP crypto.KeyPair, msgs ...[]byte) [][]byte {
	t.Helper()
	clientKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	cConn, sConn := newPipePair()
	tap := &tapConn{pipeConn: cConn}

	done := make(chan error, 1)
	go func() {
		_, err := ServerHandshake(sConn, daemonKP, func(_ []byte, secret string) bool {
			return secret == testSecret
		})
		done <- err
	}()
	client, err := ClientHandshake(tap, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatalf("capture: client handshake: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("capture: server handshake: %v", err)
	}
	for _, m := range msgs {
		if err := client.Send(m); err != nil {
			t.Fatalf("capture: send: %v", err)
		}
	}
	return tap.sent
}

// replayInto pushes recorded client frames verbatim at a fresh daemon connection and
// reports what the daemon made of it. The attacker cannot read the daemon's replies (it
// has no d2c key) and does not need to.
func replayInto(t *testing.T, daemonKP crypto.KeyPair, frames [][]byte, authorize func([]byte, string) bool) (*Conn, error) {
	t.Helper()
	cConn, sConn := newPipePair()
	// A real caller closes the socket once the handshake resolves either way; without that
	// the drain goroutine below would sit on a read forever.
	defer cConn.Close()
	defer sConn.Close()
	type res struct {
		conn *Conn
		err  error
	}
	done := make(chan res, 1)
	go func() {
		conn, err := ServerHandshake(sConn, daemonKP, authorize)
		done <- res{conn, err}
	}()
	go func() { // drain the daemon's side so nothing blocks on a full pipe
		for {
			if _, err := cConn.ReadMsg(); err != nil {
				return
			}
		}
	}()
	for _, f := range frames {
		if err := cConn.WriteMsg(f); err != nil {
			break
		}
	}
	select {
	case r := <-done:
		return r.conn, r.err
	case <-time.After(5 * time.Second):
		return nil, errors.New("daemon neither accepted nor rejected the replay")
	}
}

// The headline property: a recorded session replayed into a fresh connection must not
// authenticate. It must fail at DECRYPTION — before authorize is consulted — because the
// daemon's fresh challenge means the recorded proof was sealed under keys that no longer
// exist.
func TestReplay_CapturedHandshakeIsRejected(t *testing.T) {
	daemonKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	frames := captureSession(t, daemonKP, []byte(`{"type":"run.test","cmd":"rm -rf /"}`))
	if len(frames) < 3 {
		t.Fatalf("expected hello + proof + command on the wire, got %d frames", len(frames))
	}

	var authorizeCalls int32
	conn, err := replayInto(t, daemonKP, frames, func(_ []byte, secret string) bool {
		atomic.AddInt32(&authorizeCalls, 1)
		return secret == testSecret // the real daemon would still say yes to the real secret
	})
	if err == nil {
		t.Fatal("a recorded session replayed into a fresh connection was ACCEPTED")
	}
	if conn != nil {
		t.Fatal("a rejected replay must not yield a usable Conn")
	}
	if n := atomic.LoadInt32(&authorizeCalls); n != 0 {
		t.Fatalf("the replayed proof reached authorize %d time(s); it must not even decrypt — "+
			"if it decrypts, the only thing standing between a recording and a shell is the secret check", n)
	}
}

// Replaying the hello alone (a fresh handshake attempt with a captured public key) earns
// the attacker a NEW challenge, for which it cannot produce a proof.
func TestReplay_HelloAloneCannotProduceAProof(t *testing.T) {
	daemonKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	frames := captureSession(t, daemonKP)

	var challenges [][]byte
	for i := 0; i < 2; i++ {
		cConn, sConn := newPipePair()
		go func() { _, _ = ServerHandshake(sConn, daemonKP, func([]byte, string) bool { return true }) }()
		if err := cConn.WriteMsg(frames[0]); err != nil { // the captured hello, verbatim
			t.Fatal(err)
		}
		raw, err := cConn.ReadMsg()
		if err != nil {
			t.Fatal(err)
		}
		var sc serverChallenge
		if err := json.Unmarshal(raw, &sc); err != nil || sc.V != HandshakeV1 {
			t.Fatalf("expected a v1 challenge, got %q", raw)
		}
		c, err := hex.DecodeString(sc.Challenge)
		if err != nil || len(c) != crypto.ChallengeSize {
			t.Fatalf("bad challenge %q: %v", sc.Challenge, err)
		}
		challenges = append(challenges, c)
		cConn.Close()
		sConn.Close() // release the daemon goroutine, which is now waiting for a proof
	}
	if bytes.Equal(challenges[0], challenges[1]) {
		t.Fatal("the daemon issued the same challenge twice — the challenge must be fresh per connection")
	}
}

// Within a live session, an active attacker echoing a frame back at the daemon must be
// rejected: the sequence number inside the sealed payload does not advance.
func TestReplay_FrameReplayedIntoLiveSessionIsRejected(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()
	tap := &tapConn{pipeConn: cConn}

	type res struct {
		conn *Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		conn, err := ServerHandshake(sConn, daemonKP, func([]byte, string) bool { return true })
		sCh <- res{conn, err}
	}()
	client, err := ClientHandshake(tap, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	sr := <-sCh
	if sr.err != nil {
		t.Fatal(sr.err)
	}
	server := sr.conn

	if err := client.Send([]byte("run once")); err != nil {
		t.Fatal(err)
	}
	got, err := server.Recv()
	if err != nil || !bytes.Equal(got, []byte("run once")) {
		t.Fatalf("first delivery = %q, err %v", got, err)
	}

	// The exact bytes that just went over the wire, pushed again.
	replayed := tap.sent[len(tap.sent)-1]
	if err := cConn.WriteMsg(replayed); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Recv(); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed frame produced err %v, want ErrReplay — the command would have run twice", err)
	}

	// And the session survives the rejection: a legitimate next frame still lands.
	if err := client.Send([]byte("run twice")); err != nil {
		t.Fatal(err)
	}
	got, err = server.Recv()
	if err != nil || !bytes.Equal(got, []byte("run twice")) {
		t.Fatalf("after a rejected replay, legitimate frame = %q, err %v", got, err)
	}
}

// Two consecutive legitimate connections must both work — and must not share a channel:
// a frame from the first must be undecryptable on the second.
func TestReplay_ConsecutiveConnectionsBothSucceedWithDistinctKeys(t *testing.T) {
	daemonKP, _ := crypto.GenerateKeyPair()

	connect := func() (*Conn, *Conn, *tapConn) {
		t.Helper()
		clientKP, _ := crypto.GenerateKeyPair()
		cConn, sConn := newPipePair()
		tap := &tapConn{pipeConn: cConn}
		type res struct {
			conn *Conn
			err  error
		}
		sCh := make(chan res, 1)
		go func() {
			conn, err := ServerHandshake(sConn, daemonKP, func([]byte, string) bool { return true })
			sCh <- res{conn, err}
		}()
		client, err := ClientHandshake(tap, clientKP, daemonKP.Public(), testSecret)
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
		sr := <-sCh
		if sr.err != nil {
			t.Fatalf("server handshake: %v", sr.err)
		}
		if client.HandshakeVersion() != HandshakeV1 || sr.conn.HandshakeVersion() != HandshakeV1 {
			t.Fatalf("expected v1 on both ends, got client=%d server=%d", client.HandshakeVersion(), sr.conn.HandshakeVersion())
		}
		return client, sr.conn, tap
	}

	c1, s1, tap1 := connect()
	if err := c1.Send([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if got, err := s1.Recv(); err != nil || !bytes.Equal(got, []byte("first")) {
		t.Fatalf("connection 1 = %q, err %v", got, err)
	}
	frameFrom1 := tap1.sent[len(tap1.sent)-1]

	c2, s2, _ := connect()
	if err := c2.Send([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if got, err := s2.Recv(); err != nil || !bytes.Equal(got, []byte("second")) {
		t.Fatalf("connection 2 = %q, err %v", got, err)
	}

	if _, err := s2.openFrame(frameFrom1); err == nil {
		t.Fatal("a frame from the previous connection decrypted on the new one — the challenge is not reaching the keys")
	}
}

// --- compatibility ---------------------------------------------------------------

// legacyClientHandshakeForTest is the client half exactly as it shipped before v1: a hello
// with no version, static-static keys, the bare secret as the first sealed frame. It is
// duplicated here rather than called through ClientHandshake because the thing under test
// is what an app build ALREADY IN THE WILD does — that code is gone from this package, so
// the compatibility claim can only be tested against a copy of it.
func legacyClientHandshakeForTest(mc MsgConn, kp crypto.KeyPair, daemonPub []byte, secret string) (*Conn, error) {
	if err := writeJSON(mc, struct {
		ClientPub string `json:"client_pub"`
	}{hex.EncodeToString(kp.Public())}); err != nil {
		return nil, err
	}
	keys, err := crypto.DeriveSessionKeys(kp, daemonPub)
	if err != nil {
		return nil, err
	}
	conn, err := newConn(mc, keys.C2D, keys.D2C, HandshakeV0)
	if err != nil {
		return nil, err
	}
	if err := conn.Send([]byte(secret)); err != nil {
		return nil, err
	}
	raw, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	if err := checkVerdict(raw); err != nil {
		return nil, err
	}
	return conn, nil
}

// An app already in the wild must keep working against an upgraded daemon.
func TestCompat_LegacyClientAgainstNewDaemon(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()

	type res struct {
		conn *Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		conn, err := ServerHandshake(sConn, daemonKP, func(_ []byte, secret string) bool { return secret == testSecret })
		sCh <- res{conn, err}
	}()
	client, err := legacyClientHandshakeForTest(cConn, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatalf("a pre-v1 client must still connect during the transition: %v", err)
	}
	sr := <-sCh
	if sr.err != nil {
		t.Fatalf("server: %v", sr.err)
	}
	if sr.conn.HandshakeVersion() != HandshakeV0 {
		t.Fatalf("server version = %d, want v0 for a legacy client", sr.conn.HandshakeVersion())
	}
	if err := client.Send([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got, err := sr.conn.Recv(); err != nil || !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("legacy exchange = %q, err %v", got, err)
	}
}

// The honest half of the compatibility story: while v0 is accepted, a stream recorded from
// a pre-v1 client STILL replays. Nothing in v1 can change that — a daemon cannot tell an
// old app from an attacker imitating one. Only strict mode closes it, which is why strict
// mode exists and why this test asserts both halves.
func TestCompat_LegacyStreamStillReplaysUntilStrictMode(t *testing.T) {
	daemonKP, _ := crypto.GenerateKeyPair()
	clientKP, _ := crypto.GenerateKeyPair()

	// Capture a legacy session.
	cConn, sConn := newPipePair()
	tap := &tapConn{pipeConn: cConn}
	go func() {
		_, _ = ServerHandshake(sConn, daemonKP, func(_ []byte, secret string) bool { return secret == testSecret })
	}()
	client, err := legacyClientHandshakeForTest(tap, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send([]byte(`{"type":"run.test"}`)); err != nil {
		t.Fatal(err)
	}
	frames := tap.sent

	// Transition mode: the replay works. This is the exposure that remains until every
	// client is upgraded.
	if conn, err := replayInto(t, daemonKP, frames, func(_ []byte, secret string) bool { return secret == testSecret }); err != nil || conn == nil {
		t.Fatalf("expected the legacy replay to still succeed in transition mode (documenting the residual exposure), got %v", err)
	}

	// Strict mode: refused, and refused at the version check — authorize is never reached.
	defer func(prev bool) { AllowLegacyHandshake = prev }(AllowLegacyHandshake)
	AllowLegacyHandshake = false

	var authorizeCalls int32
	conn, err := replayInto(t, daemonKP, frames, func(_ []byte, secret string) bool {
		atomic.AddInt32(&authorizeCalls, 1)
		return secret == testSecret
	})
	if !errors.Is(err, ErrLegacyRejected) {
		t.Fatalf("strict mode: err = %v, want ErrLegacyRejected", err)
	}
	if conn != nil {
		t.Fatal("strict mode returned a Conn")
	}
	if n := atomic.LoadInt32(&authorizeCalls); n != 0 {
		t.Fatalf("strict mode consulted authorize %d time(s); it must refuse on the version alone", n)
	}
}

// A v1 client against a daemon too old to send a challenge: it must give up waiting and
// complete the pre-v1 handshake rather than hanging.
func TestCompat_NewClientAgainstLegacyDaemon(t *testing.T) {
	defer func(prev time.Duration) { legacyFallbackDelay = prev }(legacyFallbackDelay)
	legacyFallbackDelay = 50 * time.Millisecond

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()

	type res struct {
		conn *Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		// A pre-v1 daemon: read the hello (ignoring the version field it does not know
		// about), then wait for the sealed secret. legacyServerHandshake IS that code.
		var hello clientHello
		if err := readJSON(sConn, &hello); err != nil {
			sCh <- res{nil, err}
			return
		}
		clientPub, err := hex.DecodeString(hello.ClientPub)
		if err != nil {
			sCh <- res{nil, err}
			return
		}
		keys, err := crypto.DeriveSessionKeys(daemonKP, clientPub)
		if err != nil {
			sCh <- res{nil, err}
			return
		}
		conn, err := legacyServerHandshake(sConn, keys, clientPub, func(_ []byte, secret string) bool {
			return secret == testSecret
		})
		sCh <- res{conn, err}
	}()

	client, err := ClientHandshake(cConn, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatalf("a v1 client must fall back to a pre-v1 daemon, not hang: %v", err)
	}
	if client.HandshakeVersion() != HandshakeV0 {
		t.Fatalf("client version = %d, want v0 after the fallback", client.HandshakeVersion())
	}
	sr := <-sCh
	if sr.err != nil {
		t.Fatalf("legacy daemon: %v", sr.err)
	}
	if err := client.Send([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got, err := sr.conn.Recv(); err != nil || !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("fallback exchange = %q, err %v", got, err)
	}
}

// slowFirstWrite delays a connection's first outgoing frame, which is how a v1 daemon looks
// to a client on a link slower than the fallback delay.
type slowFirstWrite struct {
	*pipeConn
	delay time.Duration
	done  bool
}

func (s *slowFirstWrite) WriteMsg(b []byte) error {
	if !s.done {
		s.done = true
		time.Sleep(s.delay)
	}
	return s.pipeConn.WriteMsg(b)
}

// If the client gives up on a challenge that was merely slow, the daemon must recognise the
// v0 proof it gets instead and finish the handshake there rather than dropping a legitimate
// client. The connection succeeds WITHOUT replay protection, which is the cost of the
// heuristic and the reason the fallback delay is generous.
func TestCompat_DaemonRecoversFromSpuriousFallback(t *testing.T) {
	defer func(prev time.Duration) { legacyFallbackDelay = prev }(legacyFallbackDelay)
	legacyFallbackDelay = 5 * time.Millisecond

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()
	slow := &slowFirstWrite{pipeConn: sConn, delay: 400 * time.Millisecond}

	type res struct {
		conn *Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		conn, err := ServerHandshake(slow, daemonKP, func(_ []byte, secret string) bool { return secret == testSecret })
		sCh <- res{conn, err}
	}()

	client, err := ClientHandshake(cConn, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatalf("client must survive a challenge that arrives late: %v", err)
	}
	sr := <-sCh
	if sr.err != nil {
		t.Fatalf("daemon must recover from the client's early fallback: %v", sr.err)
	}
	if client.HandshakeVersion() != HandshakeV0 || sr.conn.HandshakeVersion() != HandshakeV0 {
		t.Fatalf("both ends must converge on v0, got client=%d server=%d", client.HandshakeVersion(), sr.conn.HandshakeVersion())
	}
	if err := client.Send([]byte("still works")); err != nil {
		t.Fatal(err)
	}
	if got, err := sr.conn.Recv(); err != nil || !bytes.Equal(got, []byte("still works")) {
		t.Fatalf("recovered exchange = %q, err %v", got, err)
	}
}

// Under strict mode there is no recovery: a v0 proof is a v0 proof.
func TestCompat_StrictModeRefusesTheFallbackRecovery(t *testing.T) {
	defer func(prevDelay time.Duration, prevLegacy bool) {
		legacyFallbackDelay = prevDelay
		AllowLegacyHandshake = prevLegacy
	}(legacyFallbackDelay, AllowLegacyHandshake)
	legacyFallbackDelay = 5 * time.Millisecond
	AllowLegacyHandshake = false

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()
	slow := &slowFirstWrite{pipeConn: sConn, delay: 400 * time.Millisecond}

	errCh := make(chan error, 1)
	go func() {
		_, err := ServerHandshake(slow, daemonKP, func([]byte, string) bool { return true })
		errCh <- err
	}()
	cErr := make(chan error, 1)
	go func() {
		_, err := ClientHandshake(cConn, clientKP, daemonKP.Public(), testSecret)
		cErr <- err
	}()

	if err := <-errCh; err == nil {
		t.Fatal("strict mode daemon accepted a v0 proof")
	}
	// The daemon dropped the connection; the client is still waiting on a verdict that will
	// never come, exactly as it would be against a real socket that just went away.
	cConn.Close()
	if err := <-cErr; err == nil {
		t.Fatal("client reported success against a daemon that refused it")
	}
}

// The sequence number is per direction and per connection: the daemon's own frames start at
// 0 too, and neither side's counter can be used to skip the other's.
func TestSequence_IsPerDirection(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()

	type res struct {
		conn *Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		conn, err := ServerHandshake(sConn, daemonKP, func([]byte, string) bool { return true })
		sCh <- res{conn, err}
	}()
	client, err := ClientHandshake(cConn, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	sr := <-sCh
	if sr.err != nil {
		t.Fatal(sr.err)
	}

	for i := 0; i < 5; i++ {
		if err := client.Send([]byte("c2d")); err != nil {
			t.Fatal(err)
		}
		if _, err := sr.conn.Recv(); err != nil {
			t.Fatalf("c2d %d: %v", i, err)
		}
		if err := sr.conn.Send([]byte("d2c")); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Recv(); err != nil {
			t.Fatalf("d2c %d: %v", i, err)
		}
	}
	// The handshake consumed seq 0 in each direction, so five more frames leave both
	// senders at 6.
	if client.sendSeq != 6 || sr.conn.sendSeq != 6 {
		t.Fatalf("send sequences = client %d, server %d; want 6 each", client.sendSeq, sr.conn.sendSeq)
	}
}
