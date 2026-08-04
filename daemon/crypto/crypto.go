// Package crypto implements the Oculus end-to-end-encrypted channel primitives.
//
// Scheme (locked, see ../../docs/plan-native-ade.md):
//
//	key agreement:  X25519 ECDH (static-static for a paired channel)
//	key derivation: HKDF-SHA256 -> two directional 32-byte keys (c2d, d2c)
//	channel AEAD:   ChaCha20-Poly1305, 12-byte random nonce, nonce prefixed to frame
//
// These primitives are chosen to interop byte-for-byte with Swift CryptoKit
// (Curve25519.KeyAgreement, HKDF<SHA256>, ChaChaPoly). Parity is locked by the
// golden vectors in ../../protocol/vectors (both sides validate them).
//
// TWO KEY SCHEDULES LIVE HERE, and the difference is the whole point:
//
//   - DeriveSessionKeys (v0, shipped) derives from static-static ECDH alone. The
//     resulting key is a pure function of the two long-term keys, so it is IDENTICAL
//     for every connection that pair ever makes. A recorded client->daemon stream
//     therefore replays verbatim into a fresh connection and authenticates: the
//     daemon contributes nothing the attacker has to guess. See
//     ../../docs/security-interception-review.md §4.3.
//   - DeriveSessionKeysV1 mixes a 32-byte challenge the DAEMON generates per
//     connection into the derivation, over a transcript that also binds both public
//     keys. Two connections between the same pair now have unrelated keys, so nothing
//     recorded from one opens under the other. That is what kills replay.
//
// v0 is retained only so that app versions already in the wild keep working; see
// transport.AllowLegacyHandshake for the switch that ends the transition.
//
// NOTE: neither schedule has forward secrecy — both endpoints' keys are long-term, so
// one future read of ~/.oculus/key decrypts every session ever recorded (§4.4). Closing
// that needs an ephemeral handshake (Noise IK/XX), which is a separate, larger change:
// see the note on DeriveSessionKeysV1.
package crypto

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

var curve = ecdh.X25519()

// HKDF labels — part of the wire contract; changing these breaks interop.
var (
	hkdfSalt    = []byte("oculus/v0 handshake")
	hkdfInfoC2D = []byte("oculus/v0 c2d")
	hkdfInfoD2C = []byte("oculus/v0 d2c")

	// v1 labels are distinct from v0's on purpose: even if a challenge were somehow all
	// zeroes, a v1 key can never collide with the v0 key for the same pair, so a v0 frame
	// can never open on a v1 channel (or vice versa). The version is therefore part of
	// what the key commits to, not just a number in a JSON field.
	hkdfSaltV1       = []byte("oculus/v1 handshake")
	hkdfInfoV1C2D    = []byte("oculus/v1 c2d")
	hkdfInfoV1D2C    = []byte("oculus/v1 d2c")
	transcriptLabel1 = []byte("oculus/v1 transcript")
)

// ChallengeSize is the length of the daemon's per-connection handshake challenge.
// 32 bytes is far past the point where a birthday collision between two connections
// is conceivable, which is what lets us treat "same challenge" as "same connection".
const ChallengeSize = 32

// X25519 computes the raw X25519 function (scalar * u-coordinate). Exposed so the
// RFC 7748 known-answer test can pin standards-compliance.
func X25519(scalar, u []byte) ([]byte, error) {
	return curve25519.X25519(scalar, u)
}

// KeyPair is an X25519 key pair.
type KeyPair struct{ priv *ecdh.PrivateKey }

// GenerateKeyPair returns a fresh random X25519 key pair.
func GenerateKeyPair() (KeyPair, error) {
	p, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{priv: p}, nil
}

// KeyPairFromPrivate rebuilds a key pair from 32 raw private-key bytes (for tests/vectors).
func KeyPairFromPrivate(priv []byte) (KeyPair, error) {
	p, err := curve.NewPrivateKey(priv)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{priv: p}, nil
}

// Public returns the 32-byte X25519 public key.
func (k KeyPair) Public() []byte { return k.priv.PublicKey().Bytes() }

// PrivateBytes returns the 32-byte X25519 private key (persist to disk to keep a
// stable daemon identity; keep it secret — file mode 0600).
func (k KeyPair) PrivateBytes() []byte { return k.priv.Bytes() }

// SessionKeys are the two directional AEAD keys for a paired channel.
// C2D encrypts client->daemon traffic; D2C encrypts daemon->client traffic.
type SessionKeys struct {
	C2D []byte
	D2C []byte
}

// DeriveSessionKeys performs X25519 ECDH between the local private key and the
// remote public key, then HKDF-SHA256 to two directional 32-byte keys. Because
// ECDH is symmetric and the HKDF salt/info labels are fixed, both endpoints derive
// identical SessionKeys.
func DeriveSessionKeys(local KeyPair, remotePub []byte) (SessionKeys, error) {
	rp, err := curve.NewPublicKey(remotePub)
	if err != nil {
		return SessionKeys{}, err
	}
	shared, err := local.priv.ECDH(rp)
	if err != nil {
		return SessionKeys{}, err
	}
	c2d, err := hkdf32(shared, hkdfInfoC2D)
	if err != nil {
		return SessionKeys{}, err
	}
	d2c, err := hkdf32(shared, hkdfInfoD2C)
	if err != nil {
		return SessionKeys{}, err
	}
	return SessionKeys{C2D: c2d, D2C: d2c}, nil
}

// GenerateChallenge returns ChallengeSize cryptographically random bytes. The daemon
// generates one per accepted connection and sends it in the clear — it is a public
// nonce, not a secret: an attacker who reads it still cannot derive anything without
// the ECDH shared secret. Its only job is to be UNPREDICTABLE, so that a stream
// recorded under an earlier challenge cannot be made to fit a later connection.
func GenerateChallenge() ([]byte, error) {
	c := make([]byte, ChallengeSize)
	if _, err := rand.Read(c); err != nil {
		return nil, err
	}
	return c, nil
}

// HandshakeTranscript hashes the public inputs of one v1 handshake:
//
//	SHA256("oculus/v1 transcript" || clientPub || daemonPub || challenge)
//
// All three inputs are exactly 32 bytes, so the concatenation is unambiguous (there is
// no length-prefix needed and no pair of different inputs that serializes the same way);
// the function rejects anything that is not 32 bytes rather than silently hashing a
// short field and losing that property.
//
// Binding BOTH public keys, not just the challenge, is what stops a transcript from
// being reinterpreted: keys derived for (clientA, daemon) can never be the keys for
// (clientB, daemon) even if an attacker could somehow force the same challenge and
// shared secret. The daemon does not learn anything new from this — it is the same data
// it already saw — but the derivation now commits to it.
func HandshakeTranscript(clientPub, daemonPub, challenge []byte) ([]byte, error) {
	for _, f := range [][]byte{clientPub, daemonPub} {
		if len(f) != 32 {
			return nil, fmt.Errorf("oculus/crypto: transcript public key must be 32 bytes, got %d", len(f))
		}
	}
	if len(challenge) != ChallengeSize {
		return nil, fmt.Errorf("oculus/crypto: transcript challenge must be %d bytes, got %d", ChallengeSize, len(challenge))
	}
	h := sha256.New()
	h.Write(transcriptLabel1)
	h.Write(clientPub)
	h.Write(daemonPub)
	h.Write(challenge)
	return h.Sum(nil), nil
}

// DeriveSessionKeysV1 is DeriveSessionKeys with the handshake transcript mixed in: the
// same X25519 ECDH, then HKDF-SHA256 with the v1 labels and the transcript appended to
// the info string. Both endpoints derive identical keys because every input is either
// symmetric (the ECDH secret) or public and agreed (the two public keys, the challenge).
//
// The caller passes clientPub/daemonPub BY ROLE rather than as local/remote, because the
// transcript is order-sensitive and the two ends see local/remote swapped. On the client
// local is the client key and remotePub is the daemon's; on the daemon it is the reverse;
// the transcript must come out the same either way, and naming the roles is what makes
// that reviewable at each call site instead of a comment you have to trust.
//
// Why this and not an ephemeral (Noise) handshake: this closes replay only, not forward
// secrecy. The honest reason is availability of vetted code — the daemon could use
// flynn/noise, but the app has CryptoKit and no audited Noise implementation, and a
// hand-assembled ephemeral handshake (transcript hashing, key confirmation, identity
// hiding, downgrade rules) is a larger risk than the hole it would close. Forward
// secrecy stays tracked as its own change: doing it properly means one Noise
// implementation on each side, not two more ECDH calls bolted onto this function.
func DeriveSessionKeysV1(local KeyPair, remotePub, clientPub, daemonPub, challenge []byte) (SessionKeys, error) {
	transcript, err := HandshakeTranscript(clientPub, daemonPub, challenge)
	if err != nil {
		return SessionKeys{}, err
	}
	rp, err := curve.NewPublicKey(remotePub)
	if err != nil {
		return SessionKeys{}, err
	}
	shared, err := local.priv.ECDH(rp)
	if err != nil {
		return SessionKeys{}, err
	}
	c2d, err := hkdf32v1(shared, append(append([]byte{}, hkdfInfoV1C2D...), transcript...))
	if err != nil {
		return SessionKeys{}, err
	}
	d2c, err := hkdf32v1(shared, append(append([]byte{}, hkdfInfoV1D2C...), transcript...))
	if err != nil {
		return SessionKeys{}, err
	}
	return SessionKeys{C2D: c2d, D2C: d2c}, nil
}

func hkdf32(secret, info []byte) ([]byte, error) {
	return hkdfRead(secret, hkdfSalt, info)
}

func hkdf32v1(secret, info []byte) ([]byte, error) {
	return hkdfRead(secret, hkdfSaltV1, info)
}

func hkdfRead(secret, salt, info []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, secret, salt, info)
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

const nonceSize = chacha20poly1305.NonceSize // 12

// Sealer encrypts messages with a fresh random 12-byte nonce per message.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer creates a Sealer for a 32-byte directional key.
func NewSealer(key []byte) (*Sealer, error) {
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: a}, nil
}

// Seal encrypts plaintext and returns nonce||ciphertext with a fresh random nonce.
//
// NONCES ARE RANDOM, DELIBERATELY, AND THE REASON CHANGED WITH v1 — read this before
// "fixing" it to a counter:
//
// Under the v0 schedule the channel key is static-static (stable per pairing, never
// rotated), so a counter reset to 0 on each new session would reuse (key, nonce) across
// sessions — a catastrophic break for ChaCha20-Poly1305 (XOR of two plaintexts, and a
// forgeable Poly1305 key). That argument alone forced random nonces.
//
// Under v1 (DeriveSessionKeysV1) that specific hazard is gone: the key commits to a
// fresh 32-byte daemon challenge, so two connections never share a key and a counter
// restarting at 0 would be safe. We still use random nonces, for two reasons:
//
//  1. Blast radius. A counter makes AEAD safety depend on challenge uniqueness — a bug
//     that ever reuses or fails to refresh a challenge (a broken RNG, a cached
//     handshake, a future "resume" feature, someone reintroducing a static path) turns
//     a key-reuse bug into a full plaintext-recovery break. With random nonces the same
//     bug is merely a replay bug, which the sequence check in ../transport already
//     rejects. Random nonces fail soft; counters fail catastrophically.
//  2. Replay protection does not need it. Ordering and duplicate rejection come from the
//     authenticated sequence number the transport puts inside the sealed payload, not
//     from the nonce, so a counter would buy nothing here.
//
// A random 96-bit nonce's collision probability stays negligible well past any realistic
// message volume, it needs no per-session state to survive restarts, and it is inherently
// safe under concurrent Seal calls (no shared mutable counter to race).
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	frame := make([]byte, nonceSize, nonceSize+len(plaintext)+s.aead.Overhead())
	if _, err := rand.Read(frame[:nonceSize]); err != nil {
		return nil, err
	}
	return s.aead.Seal(frame, frame[:nonceSize], plaintext, nil), nil
}

// Opener decrypts frames produced by a Sealer sharing the same directional key.
type Opener struct {
	aead cipher.AEAD
}

// NewOpener creates an Opener for a 32-byte directional key.
func NewOpener(key []byte) (*Opener, error) {
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &Opener{aead: a}, nil
}

// ErrShortFrame is returned when a frame is too small to contain a nonce.
var ErrShortFrame = errors.New("oculus/crypto: frame shorter than nonce")

// Open decrypts a nonce||ciphertext frame. It fails if authentication fails.
func (o *Opener) Open(frame []byte) ([]byte, error) {
	if len(frame) < nonceSize {
		return nil, ErrShortFrame
	}
	nonce := frame[:nonceSize]
	ciphertext := frame[nonceSize:]
	return o.aead.Open(nil, nonce, ciphertext, nil)
}
