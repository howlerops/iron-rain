// Package crypto implements the Oculus end-to-end-encrypted channel primitives.
//
// Scheme (locked, see ../../docs/plan-native-ade.md):
//
//	key agreement:  X25519 ECDH (static-static for a paired channel)
//	key derivation: HKDF-SHA256 -> two directional 32-byte keys (c2d, d2c)
//	channel AEAD:   ChaCha20-Poly1305, 12-byte counter nonce, nonce prefixed to frame
//
// These primitives are chosen to interop byte-for-byte with Swift CryptoKit
// (Curve25519.KeyAgreement, HKDF<SHA256>, ChaChaPoly). Parity is locked by the
// golden vectors in ../../protocol/vectors (both sides validate them).
//
// NOTE: static-static ECDH gives a stable paired key but no forward secrecy.
// v0 accepts this; an ephemeral handshake (forward secrecy) is a tracked follow-up.
package crypto

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
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
)

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

func hkdf32(secret, info []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, secret, hkdfSalt, info)
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

const nonceSize = chacha20poly1305.NonceSize // 12

// Sealer encrypts an ordered stream of messages with a per-message counter nonce.
type Sealer struct {
	aead    cipher.AEAD
	counter uint64
}

// NewSealer creates a Sealer for a 32-byte directional key.
func NewSealer(key []byte) (*Sealer, error) {
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: a}, nil
}

// Seal encrypts plaintext and returns nonce||ciphertext. Each call advances the nonce.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	binary.BigEndian.PutUint64(nonce[nonceSize-8:], s.counter)
	s.counter++
	frame := make([]byte, nonceSize, nonceSize+len(plaintext)+s.aead.Overhead())
	copy(frame, nonce)
	return s.aead.Seal(frame, nonce, plaintext, nil), nil
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
