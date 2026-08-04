package relay

// Proof of possession for the relay's host slot.
//
// WHY: the relay handed out the host slot for a server_id to anyone who could NAME that server_id —
// and the server_id IS the daemon's PUBLIC key. It is printed to stdout on every daemon start
// (main.go's "server id" line), embedded in every pairing QR, stored in ~/.oculus/pairing.json, and
// written to the relay operator's own request logs. "Knows the server_id" was never a secret, so
// registration was effectively unauthenticated. Anyone who had seen a QR over the user's shoulder
// could register as the host, evict the real daemon (whose re-dial loop re-registers and is evicted
// again, forever), and sit in the bridge position.
//
// What that costs the user is NOT confidentiality — the channel is sealed to the daemon's key and
// the impostor holds no private key, so clients that reach it simply fail their handshake. It costs
// availability (remote access is dead and looks like "the relay is flaky"), and it hands the
// attacker a recording position — which matters more than it sounds, because the transport
// contributes no server randomness and derives the same static keys every session, so a recorded
// client->daemon stream can be replayed at the real daemon later
// (docs/security-interception-review.md §4.2, §4.3).
//
// The fix is to make claiming the slot cost the PRIVATE key instead of the public one. The daemon's
// identity key is X25519 — an agreement key with no signature scheme attached — so the proof is an
// ECDH one rather than a signature:
//
//	relay -> host   {"ir":"pop-challenge","eph":<fresh X25519 public key>,"nonce":<32 random bytes>}
//	host  -> relay  {"ir":"pop-proof","mac":HMAC(prk, nonce || sid)}
//	                where shared = X25519(daemonPriv, eph), prk = HMAC("iron-rain/relay-pop/v1", shared)
//	relay           computes the same shared as X25519(ephPriv, sid) and compares
//
// Only the holder of the private key behind sid can compute that shared secret, so the MAC is
// unforgeable by someone holding the public value alone. Both eph and nonce are fresh per
// connection, so a proof recorded off the wire is worthless against the next challenge — this
// mechanism deliberately does not inherit the transport's replay gap. The relay learns nothing new:
// the shared secret is with an ephemeral key it discards, and the daemon's public key was already
// in the URL it routed on.
//
// It is OPT-IN, signalled by ?pop=1 on the host registration, and that is not negotiable-away
// laziness: daemons already in the field do not implement this, and a relay that DEMANDED a proof
// would take every one of them offline until its user happened to update. What opting in buys is
// the one rule in claimHost — a proven host can never be evicted by an unproven one — so a daemon
// that has updated is permanently safe from the hijack even though the relay still serves daemons
// that have not.
//
// It does nothing for the client slot (§4.2 point 3): a client's key is generated fresh on every app
// launch and pinned nowhere, so there is no value the relay could challenge it against. A second
// role=client registration still evicts the first.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/curve25519"
)

const (
	// popQuery is the host's opt-in signal. Absent (every deployed daemon today) means the host
	// registers unproven and the relay behaves exactly as it did before.
	popQuery = "pop"

	popChallenge = "pop-challenge"
	popProof     = "pop-proof"

	// popLabel domain-separates the proof key from every other use of an X25519 shared secret in
	// this system. It is part of the wire contract, shared with relay-cf/src/index.ts; changing it
	// on one side and not the other rejects every proof.
	popLabel = "iron-rain/relay-pop/v1"

	popNonceLen = 32

	// defaultPopTimeout bounds the relay's wait for the proof. A host that opts in and then says
	// nothing must not park a goroutine and a socket, for the same slowloris reason as
	// defaultRegistrationTimeout — and it is generous enough for a round trip to a daemon behind a
	// consumer uplink.
	defaultPopTimeout = 10 * time.Second
)

var (
	errPopBadSID   = errors.New("relay: ?pop=1 requires a 32-byte hex public-key sid")
	errPopBadFrame = errors.New("relay: malformed proof-of-possession frame")
	errPopMismatch = errors.New("relay: proof of possession did not verify")
)

// popMsg is the challenge/proof wire frame. It travels as a WebSocket TEXT message; session traffic
// is binary, which is what lets a host distinguish relay control frames from a peer's ciphertext
// without a length-prefixed sub-protocol.
type popMsg struct {
	IR    string `json:"ir"`
	V     int    `json:"v"`
	Eph   string `json:"eph,omitempty"`
	Nonce string `json:"nonce,omitempty"`
	MAC   string `json:"mac,omitempty"`
}

// popMAC derives the proof from an X25519 shared secret. Two HMAC-SHA256 passes rather than HKDF:
// the first is an extract step that domain-separates the raw curve output, the second binds the
// answer to this challenge (nonce) and this slot (sid). HMAC keeps the mirror implementation in the
// Cloudflare Worker to two WebCrypto calls with no HKDF to get wrong.
func popMAC(shared, nonce, sid []byte) []byte {
	prk := hmac.New(sha256.New, []byte(popLabel))
	prk.Write(shared)
	mac := hmac.New(sha256.New, prk.Sum(nil))
	mac.Write(nonce)
	mac.Write(sid)
	return mac.Sum(nil)
}

// decodePub decodes a server_id into the 32-byte X25519 public key it is supposed to be.
func decodePub(sid string) ([]byte, error) {
	pub, err := hex.DecodeString(sid)
	if err != nil || len(pub) != curve25519.PointSize {
		return nil, errPopBadSID
	}
	return pub, nil
}

// verifyHostPossession runs the relay half of the exchange on a freshly accepted host socket,
// BEFORE the slot is claimed — the whole point is to decide the claim, so it cannot happen later.
// It returns nil only if the peer proved it holds the private key behind sid.
func verifyHostPossession(ctx context.Context, ws *websocket.Conn, sid string, timeout time.Duration) error {
	pub, err := decodePub(sid)
	if err != nil {
		return err
	}
	ephPriv := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(ephPriv); err != nil {
		return err
	}
	ephPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return err
	}
	nonce := make([]byte, popNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	challenge, err := json.Marshal(popMsg{IR: popChallenge, V: 1, Eph: hex.EncodeToString(ephPub), Nonce: hex.EncodeToString(nonce)})
	if err != nil {
		return err
	}
	if err := ws.Write(wctx, websocket.MessageText, challenge); err != nil {
		return err
	}

	typ, data, err := ws.Read(wctx)
	if err != nil {
		return err
	}
	var msg popMsg
	if typ != websocket.MessageText || json.Unmarshal(data, &msg) != nil || msg.IR != popProof {
		return errPopBadFrame
	}
	got, err := hex.DecodeString(msg.MAC)
	if err != nil {
		return errPopBadFrame
	}
	// X25519 errors on a shared secret of all zeroes (a low-order eph would be the attacker's own
	// input here, not ours, but the check costs nothing and keeps the failure explicit).
	shared, err := curve25519.X25519(ephPriv, pub)
	if err != nil {
		return err
	}
	if !hmac.Equal(got, popMAC(shared, nonce, pub)) {
		return errPopMismatch
	}
	return nil
}

// answerHostChallenge computes the daemon half. Errors are the caller's cue to give up on this
// socket; the re-dial loop will get a fresh challenge.
func answerHostChallenge(msg popMsg, sid, hostPriv []byte) ([]byte, error) {
	eph, err := hex.DecodeString(msg.Eph)
	if err != nil || len(eph) != curve25519.PointSize {
		return nil, errPopBadFrame
	}
	nonce, err := hex.DecodeString(msg.Nonce)
	if err != nil || len(nonce) == 0 {
		return nil, errPopBadFrame
	}
	shared, err := curve25519.X25519(hostPriv, eph)
	if err != nil {
		return nil, err
	}
	return json.Marshal(popMsg{IR: popProof, V: 1, MAC: hex.EncodeToString(popMAC(shared, nonce, sid))})
}

// popCanProve reports whether hostPriv is actually the private key behind serverID, and returns the
// decoded public key.
//
// A caller that passes a key which does NOT match the server_id it is registering (a wiring
// mistake, or a test using a symbolic id like "srv-1") would opt into a challenge it cannot answer
// and be closed by the relay on every dial — turning a misconfiguration into a total remote-access
// outage. Registering unproven instead degrades to exactly today's behaviour, which is the failure
// mode worth having.
func popCanProve(serverID string, hostPriv []byte) ([]byte, bool) {
	if len(hostPriv) != curve25519.ScalarSize {
		return nil, false
	}
	want, err := decodePub(serverID)
	if err != nil {
		return nil, false
	}
	got, err := curve25519.X25519(hostPriv, curve25519.Basepoint)
	if err != nil || !bytes.Equal(got, want) {
		return nil, false
	}
	return want, true
}
