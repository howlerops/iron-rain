// Package transport carries the Oculus protocol over an encrypted channel.
//
// A MsgConn moves discrete byte messages (a WebSocket is one; tests use an
// in-memory pair). The client announces its static X25519 public key in the clear (a
// public key — safe), the daemon answers with a random per-connection challenge, both
// sides derive directional session keys from static-static ECDH bound to that challenge
// (see ../crypto), and the client then proves the pairing secret by sending it
// *encrypted* as the first sealed frame. Every message after is sealed with
// ChaCha20-Poly1305 and carries an authenticated per-connection sequence number. The
// relay, sitting on the MsgConn, only ever sees ciphertext — the pairing secret never
// transits in the clear, and a passive relay cannot verify secret guesses without a
// private key (ECDH hardness).
//
// # Why the challenge and the sequence number exist
//
// The shipped handshake (v0, still accepted — see AllowLegacyHandshake) had neither, and
// that made the whole channel replayable: the daemon contributed no randomness, so the
// client->daemon direction was a pure function of bytes an attacker already had, and the
// static-static key meant a recorded frame still opened on a fresh connection. A passive
// listener on an untrusted LAN (the direct route has no TLS) could record a session and
// later replay `client_hello` + the sealed secret + any captured command frame into the
// real daemon: it could not read the answers, but the commands RAN. See
// ../../docs/security-interception-review.md §4.3.
//
// v1 closes that at two levels, deliberately belt-and-braces:
//
//   - Across connections: the daemon's fresh challenge is mixed into the key schedule,
//     so frames recorded under one connection are undecryptable garbage under the next.
//     Replaying the recorded hello just earns the attacker a NEW challenge it cannot
//     produce a proof for. The sealed proof also carries the challenge back explicitly,
//     so the binding is checked in plaintext and not only implied by key agreement.
//   - Within a connection: every sealed payload is prefixed with a monotonic sequence
//     number under the AEAD, and a frame whose sequence does not increase is rejected.
//     That kills duplication and reordering by an active attacker who is echoing frames
//     back into a live session.
//
// What v1 does NOT do is forward secrecy: both key pairs are still long-term, so an
// attacker who records traffic and later obtains ~/.oculus/key can still decrypt
// everything recorded (§4.4). That needs an ephemeral handshake and is tracked
// separately — see the note on crypto.DeriveSessionKeysV1 for why it is not bolted on
// here.
package transport

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
)

// MsgConn is a bidirectional stream of discrete byte messages.
type MsgConn interface {
	WriteMsg([]byte) error
	ReadMsg() ([]byte, error)
	Close() error
}

// Handshake versions.
const (
	// HandshakeV0 is the shipped handshake: static-static keys, no daemon challenge, no
	// sequence numbers. Replayable by anyone who recorded a session (§4.3). Accepted only
	// so that app builds already in the wild keep connecting.
	HandshakeV0 = 0
	// HandshakeV1 binds the session keys to a daemon-generated challenge and sequences
	// every frame.
	HandshakeV1 = 1
)

// AllowLegacyHandshake decides whether ServerHandshake still accepts v0 clients.
//
// This is the deprecation lever, and it is the only thing that actually ENDS the replay
// exposure: while v0 is accepted, an attacker who recorded an old client's session can
// still replay it by simply stripping the version field from the hello it replays —
// there is no way for a daemon to distinguish "old app" from "attacker pretending to be
// an old app", because that is what backwards compatibility means. So a permissive
// default means the replay fix is present in the code and absent in practice.
//
// It now defaults to STRICT. v0 is refused unless OCULUS_ALLOW_LEGACY_HANDSHAKE=1 says
// otherwise. The escape hatch exists rather than deleting v0 outright because the failure
// mode of getting this wrong is total: an app that speaks only v0 cannot connect AT ALL,
// and "my phone stopped working" is not a state to have no recovery from. Turning it back
// on is a deliberate act with a name that says what it costs.
//
// The v1 client's silence-based fallback (legacyFallbackDelay) is what makes the strict
// default safe to hold: a v1 client meeting a v1 daemon never waits, because the daemon's
// challenge is its first frame. Only a v1 client meeting a PRE-v1 daemon pays the 3s, and
// that pairing is unaffected by this flag — the flag governs the daemon's side.
//
// Tests set it directly.
var AllowLegacyHandshake = os.Getenv("OCULUS_ALLOW_LEGACY_HANDSHAKE") == "1"

// legacyFallbackDelay is how long a v1 client waits for the daemon's challenge before
// concluding it is talking to a pre-v1 daemon.
//
// A pre-v1 daemon sends NOTHING at this point — it has read the hello and is blocked
// waiting for the sealed proof — so silence is the only signal available, and a timer is
// the only way to read it. The two ways to get it wrong are not symmetric:
//
//   - too short: a slow link makes a v1 client give up on a v1 daemon and fall back. The
//     daemon recovers (it trial-opens the v0 proof, see ServerHandshake) so the
//     connection still succeeds, but it succeeds WITHOUT replay protection.
//   - too long: every connection to a pre-v1 daemon stalls for this long before working.
//
// 3s is chosen to sit well above any plausible relay round trip while staying inside the
// app's 12s handshake budget. It exists only during the transition: once
// AllowLegacyHandshake is false everywhere, the fallback and this variable go away. It is a
// var so tests can shrink it (and force the losing side of the race on purpose).
var legacyFallbackDelay = 3 * time.Second

type clientHello struct {
	ClientPub string `json:"client_pub"`
	// V advertises the highest handshake version the client supports. Absent (0) means a
	// pre-v1 client — encoding/json leaves it at the zero value, which is exactly the
	// behaviour that makes old clients decode correctly here, and old daemons ignore this
	// field when a new client sends it.
	V int `json:"v,omitempty"`
}

// serverChallenge is the daemon's first frame in a v1 handshake, sent in the clear. The
// challenge is a public nonce: knowing it is worthless without the ECDH shared secret,
// and it must be seen by the client before the client can produce a bound proof.
type serverChallenge struct {
	V         int    `json:"v"`
	Challenge string `json:"challenge"`
}

// clientProof is the plaintext of the first sealed frame in a v1 handshake. It echoes the
// challenge so the daemon can verify the binding directly, rather than inferring it from
// the fact that the frame decrypted. Those are equivalent today; keeping the explicit
// check means a future mistake in the key schedule cannot silently reopen replay.
type clientProof struct {
	Secret    string `json:"secret"`
	Challenge string `json:"challenge"`
}

type serverHello struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ErrUnauthorized is returned by ServerHandshake when authorize rejects a client.
var ErrUnauthorized = errors.New("transport: unauthorized")

// ErrReplay is returned by Recv when a sealed frame's sequence number does not advance —
// i.e. the frame is a duplicate or was reordered by something sitting on the wire.
var ErrReplay = errors.New("transport: frame sequence did not advance (replay)")

// ErrLegacyRejected is returned by ServerHandshake when a v0 client connects and
// AllowLegacyHandshake is false.
var ErrLegacyRejected = errors.New("transport: legacy handshake refused (client too old)")

// ClientHandshake authenticates to the daemon and returns an encrypted Conn.
// daemonPub is the daemon's static public key (obtained during pairing). The
// pairing secret is sent encrypted (never in the clear over the relay).
func ClientHandshake(mc MsgConn, kp crypto.KeyPair, daemonPub []byte, secret string) (*Conn, error) {
	clientPub := kp.Public()
	// 1. announce our static public key (a public key — safe in the clear) and the
	//    handshake version we support.
	if err := writeJSON(mc, clientHello{ClientPub: hex.EncodeToString(clientPub), V: HandshakeV1}); err != nil {
		return nil, err
	}

	// 2. wait for the daemon's challenge.
	//
	// The read runs on its own goroutine so it can be timed out WITHOUT being abandoned.
	// An abandoned ReadMsg stays blocked on the socket and swallows the next frame into a
	// channel nobody reads — and on the fallback path that next frame is the verdict we
	// still need. pending stays valid across the fallback for exactly that reason.
	pending := readAsync(mc)

	var challenge []byte
	select {
	case r := <-pending.ch:
		if r.err != nil {
			return nil, r.err
		}
		var sc serverChallenge
		if err := json.Unmarshal(r.b, &sc); err != nil || sc.V < HandshakeV1 || sc.Challenge == "" {
			return nil, fmt.Errorf("transport: expected a v1 challenge as the daemon's first frame, got %d bytes that are not one", len(r.b))
		}
		c, err := hex.DecodeString(sc.Challenge)
		if err != nil || len(c) != crypto.ChallengeSize {
			return nil, fmt.Errorf("transport: bad challenge (%d bytes): %v", len(c), err)
		}
		challenge = c
	case <-time.After(legacyFallbackDelay):
		return legacyClientHandshake(mc, kp, daemonPub, secret, pending)
	}

	// 3. derive the channel from static-static ECDH bound to the challenge, so these keys
	//    exist for this connection only.
	keys, err := crypto.DeriveSessionKeysV1(kp, daemonPub, clientPub, daemonPub, challenge)
	if err != nil {
		return nil, err
	}
	conn, err := newConn(mc, keys.C2D, keys.D2C, HandshakeV1) // client sends c2d, receives d2c
	if err != nil {
		return nil, err
	}
	// 4. prove the pairing secret by sending it ENCRYPTED, bound to this challenge.
	proof, err := json.Marshal(clientProof{Secret: secret, Challenge: hex.EncodeToString(challenge)})
	if err != nil {
		return nil, err
	}
	if err := conn.Send(proof); err != nil {
		return nil, err
	}
	// 5. read the server's encrypted verdict.
	raw, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	if err := checkVerdict(raw); err != nil {
		return nil, err
	}
	return conn, nil
}

// legacyClientHandshake performs the pre-v1 handshake (no challenge, no sequencing) for a
// daemon that never sent a challenge. pending is the still-outstanding read from
// ClientHandshake: it delivers the daemon's verdict, and must not be re-issued.
func legacyClientHandshake(mc MsgConn, kp crypto.KeyPair, daemonPub []byte, secret string, pending *asyncRead) (*Conn, error) {
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

	// Normally the next frame is the verdict. But if we lost a race — a v1 daemon whose
	// challenge was merely slow — the outstanding read delivers that challenge instead,
	// and the verdict is the frame after it. The daemon recovers on its side by
	// trial-opening our v0 proof, so the connection still completes; we just have to
	// skip the stray challenge. Exactly one can be in flight, so the loop runs at most twice.
	for attempt := 0; attempt < 2; attempt++ {
		var raw []byte
		if attempt == 0 {
			r := <-pending.ch
			if r.err != nil {
				return nil, r.err
			}
			raw = r.b
		} else {
			b, err := mc.ReadMsg()
			if err != nil {
				return nil, err
			}
			raw = b
		}
		plain, err := conn.openFrame(raw)
		if err != nil {
			if attempt == 0 && isChallengeFrame(raw) {
				continue
			}
			return nil, err
		}
		if err := checkVerdict(plain); err != nil {
			return nil, err
		}
		return conn, nil
	}
	return nil, errors.New("transport: no verdict after the daemon's challenge")
}

// ServerHandshake accepts a client, checks authorize(clientPub, secret) against
// the encrypted pairing proof, and returns an encrypted Conn.
func ServerHandshake(mc MsgConn, kp crypto.KeyPair, authorize func(clientPub []byte, secret string) bool) (*Conn, error) {
	var hello clientHello
	if err := readJSON(mc, &hello); err != nil {
		return nil, err
	}
	clientPub, err := hex.DecodeString(hello.ClientPub)
	if err != nil {
		return nil, fmt.Errorf("transport: bad client_pub: %w", err)
	}
	daemonPub := kp.Public()

	// Legacy keys are derived either way: a v0 client needs them, and a v1 client that
	// timed out waiting for our challenge falls back to them (see below).
	legacyKeys, err := crypto.DeriveSessionKeys(kp, clientPub)
	if err != nil {
		return nil, err
	}

	if hello.V < HandshakeV1 {
		if !AllowLegacyHandshake {
			// Answer in the dialect the old client speaks so it shows a real error instead
			// of a dropped socket, then refuse.
			if conn, cerr := newConn(mc, legacyKeys.D2C, legacyKeys.C2D, HandshakeV0); cerr == nil {
				_ = sendHello(conn, serverHello{OK: false, Error: "client too old: upgrade required"})
			}
			return nil, ErrLegacyRejected
		}
		return legacyServerHandshake(mc, legacyKeys, clientPub, authorize)
	}

	// A client advertising a version above ours still gets our v1 challenge, which names
	// the version we actually speak; deciding what to do with that is the client's job.
	challenge, err := crypto.GenerateChallenge()
	if err != nil {
		return nil, err
	}
	if err := writeJSON(mc, serverChallenge{V: HandshakeV1, Challenge: hex.EncodeToString(challenge)}); err != nil {
		return nil, err
	}
	keys, err := crypto.DeriveSessionKeysV1(kp, clientPub, clientPub, daemonPub, challenge)
	if err != nil {
		return nil, err
	}
	conn, err := newConn(mc, keys.D2C, keys.C2D, HandshakeV1) // server sends d2c, receives c2d
	if err != nil {
		return nil, err
	}
	conn.peerPub = clientPub

	raw, err := mc.ReadMsg()
	if err != nil {
		return nil, err
	}
	plain, err := conn.openFrame(raw)
	if err != nil {
		// The client may have given up on our challenge and sent a v0 proof (a slow link,
		// see legacyFallbackDelay). It is committed to v0 now, so the only way to keep that
		// connection alive is to meet it there: trial-open the same frame under the legacy
		// keys. This is not a new weakness — it is reachable only while v0 is accepted at
		// all, and it is the same downgrade an attacker gets for free by stripping the
		// version field. Under strict mode it does not exist.
		lconn, lerr := newConn(mc, legacyKeys.D2C, legacyKeys.C2D, HandshakeV0)
		if lerr != nil {
			return nil, err
		}
		lconn.peerPub = clientPub
		lplain, lerr := lconn.openFrame(raw)
		if lerr != nil {
			return nil, err // report the v1 failure: that is the one that describes reality
		}
		// The frame opened under legacy keys, so this really is a v1 client that fell back —
		// not a corrupt frame. Under strict mode we still refuse it, but we ANSWER first, in
		// the dialect it has committed to. Returning silently here left the client blocked on
		// a verdict that was never coming: over a pipe that hangs outright, and over a socket
		// it surfaces as a bare disconnect, which is indistinguishable from a flaky network at
		// exactly the moment the user needs to be told to update. The branch above does the
		// same thing for a client that announced v0 up front; this is the same courtesy for
		// one that arrived at v0 the slow way.
		if !AllowLegacyHandshake {
			_ = sendHello(lconn, serverHello{OK: false, Error: "client too old: upgrade required"})
			return nil, ErrLegacyRejected
		}
		return finishLegacy(lconn, clientPub, string(lplain), authorize)
	}

	var proof clientProof
	if err := json.Unmarshal(plain, &proof); err != nil {
		return nil, fmt.Errorf("transport: bad handshake proof: %w", err)
	}
	echoed, err := hex.DecodeString(proof.Challenge)
	if err != nil || !bytes.Equal(echoed, challenge) {
		// Unreachable unless the key schedule is broken, since a proof sealed for another
		// challenge would not have opened. Checked anyway — see clientProof.
		_ = sendHello(conn, serverHello{OK: false, Error: "unauthorized"})
		return nil, ErrUnauthorized
	}
	if authorize == nil || !authorize(clientPub, proof.Secret) {
		_ = sendHello(conn, serverHello{OK: false, Error: "unauthorized"})
		return nil, ErrUnauthorized
	}
	if err := sendHello(conn, serverHello{OK: true}); err != nil {
		return nil, err
	}
	return conn, nil
}

// legacyServerHandshake is the pre-v1 handshake, byte-for-byte as it shipped: read the
// sealed secret as the first frame, answer with a sealed verdict. No challenge, no
// sequencing — and therefore still replayable. It exists only for clients in the wild.
func legacyServerHandshake(mc MsgConn, keys crypto.SessionKeys, clientPub []byte, authorize func([]byte, string) bool) (*Conn, error) {
	conn, err := newConn(mc, keys.D2C, keys.C2D, HandshakeV0)
	if err != nil {
		return nil, err
	}
	conn.peerPub = clientPub
	raw, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	return finishLegacy(conn, clientPub, string(raw), authorize)
}

func finishLegacy(conn *Conn, clientPub []byte, secret string, authorize func([]byte, string) bool) (*Conn, error) {
	if authorize == nil || !authorize(clientPub, secret) {
		_ = sendHello(conn, serverHello{OK: false, Error: "unauthorized"})
		return nil, ErrUnauthorized
	}
	if err := sendHello(conn, serverHello{OK: true}); err != nil {
		return nil, err
	}
	return conn, nil
}

func checkVerdict(raw []byte) error {
	var resp serverHello
	if err := json.Unmarshal(raw, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("transport: handshake rejected: %s", resp.Error)
	}
	return nil
}

// isChallengeFrame reports whether a frame that failed to decrypt is in fact a cleartext
// v1 challenge. A sealed frame is indistinguishable from random, so it parses as this
// JSON only with negligible probability; the check is a disambiguation, not a security
// boundary (nothing is trusted on the strength of it — the frame is skipped, not acted on).
func isChallengeFrame(raw []byte) bool {
	var sc serverChallenge
	return json.Unmarshal(raw, &sc) == nil && sc.V >= HandshakeV1 && sc.Challenge != ""
}

type readResult struct {
	b   []byte
	err error
}

type asyncRead struct{ ch chan readResult }

// readAsync issues one ReadMsg on its own goroutine. The channel is buffered so the
// goroutine always finishes even if nobody is waiting any more — it never leaks, and the
// frame it read is still there for whoever wants it later.
func readAsync(mc MsgConn) *asyncRead {
	r := &asyncRead{ch: make(chan readResult, 1)}
	go func() {
		b, err := mc.ReadMsg()
		r.ch <- readResult{b: b, err: err}
	}()
	return r
}

func sendHello(conn *Conn, h serverHello) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return conn.Send(b)
}

// seqLen is the width of the sequence number prefixed to every v1 payload before sealing.
// It lives INSIDE the ciphertext, so it is authenticated and invisible to the relay; a
// wire-visible counter would have handed the relay a per-connection message count for free.
const seqLen = 8

// Conn is an encrypted, message-oriented connection.
type Conn struct {
	mc      MsgConn
	sendMu  sync.Mutex
	sealer  *crypto.Sealer
	opener  *crypto.Opener
	version int
	// peerPub is the client's public key from the handshake. It identifies WHICH paired device this
	// connection is, which is what lets an invited guest be given a different role from the owner.
	peerPub []byte

	sendSeq  uint64
	recvMu   sync.Mutex
	lastRecv uint64
	haveRecv bool
}

// PeerPublicKey returns the client's handshake public key (nil for a client-side Conn).
func (c *Conn) PeerPublicKey() []byte { return append([]byte(nil), c.peerPub...) }

// HandshakeVersion reports which handshake produced this connection: HandshakeV1 has
// replay protection, HandshakeV0 does not. Callers that want to warn about — or refuse —
// a legacy client can read it here.
func (c *Conn) HandshakeVersion() int { return c.version }

func newConn(mc MsgConn, sendKey, recvKey []byte, version int) (*Conn, error) {
	sealer, err := crypto.NewSealer(sendKey)
	if err != nil {
		return nil, err
	}
	opener, err := crypto.NewOpener(recvKey)
	if err != nil {
		return nil, err
	}
	return &Conn{mc: mc, sealer: sealer, opener: opener, version: version}, nil
}

// Send encrypts and writes one message. The lock is held across both the seal and the
// write so concurrent senders can't interleave frames on the wire (which would reorder
// the ordered event stream a client replays) — and, on a v1 connection, so that the
// sequence number a frame carries is the order it actually goes out in.
func (c *Conn) Send(plaintext []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	payload := plaintext
	if c.version >= HandshakeV1 {
		payload = make([]byte, seqLen+len(plaintext))
		binary.BigEndian.PutUint64(payload[:seqLen], c.sendSeq)
		copy(payload[seqLen:], plaintext)
		// Burn the sequence number here, not after a successful write: if WriteMsg fails
		// and a caller retries, reusing the number would look exactly like a replay to the
		// peer and drop the connection. Numbers are free; there are 2^64 of them.
		c.sendSeq++
	}
	frame, err := c.sealer.Seal(payload)
	if err != nil {
		return err
	}
	return c.mc.WriteMsg(frame)
}

// Recv reads and decrypts one message.
//
// The lock covers the read as well as the sequence check: two goroutines calling Recv
// concurrently would otherwise race to claim frames and one would reject a perfectly good
// frame as out of sequence. Serializing receivers is the correct behaviour for an ordered
// stream anyway — the frames have a meaningful order, so consuming them out of order was
// never safe.
func (c *Conn) Recv() ([]byte, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	frame, err := c.mc.ReadMsg()
	if err != nil {
		return nil, err
	}
	return c.openLocked(frame)
}

// openFrame is Recv's decrypt half for a frame that has already been read off the wire.
// The handshake needs it: the daemon must read the client's proof before it knows which
// key schedule opens it (see ServerHandshake).
func (c *Conn) openFrame(frame []byte) ([]byte, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	return c.openLocked(frame)
}

func (c *Conn) openLocked(frame []byte) ([]byte, error) {
	plain, err := c.opener.Open(frame)
	if err != nil {
		return nil, err
	}
	if c.version < HandshakeV1 {
		return plain, nil
	}
	if len(plain) < seqLen {
		return nil, fmt.Errorf("transport: sealed payload shorter than its sequence number")
	}
	seq := binary.BigEndian.Uint64(plain[:seqLen])
	// Strictly increasing, not exactly-next: the rule stays true for any ordered transport
	// and needs no state beyond the last value. A duplicate or an out-of-order injection
	// fails it, which is the property we are after; a DROPPED frame is not caught here,
	// but an attacker who can drop frames can equally cut the connection.
	if c.haveRecv && seq <= c.lastRecv {
		return nil, ErrReplay
	}
	c.lastRecv, c.haveRecv = seq, true
	return plain[seqLen:], nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.mc.Close() }

func writeJSON(mc MsgConn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return mc.WriteMsg(b)
}

func readJSON(mc MsgConn, v any) error {
	b, err := mc.ReadMsg()
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
