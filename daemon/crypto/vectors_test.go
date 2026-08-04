package crypto

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// The golden vectors are the cross-language contract: the Swift/CryptoKit client
// MUST reproduce every field here. Regenerate with:
//
//	OCULUS_UPDATE_VECTORS=1 go test ./crypto/ -run TestHandshakeGoldenVectors
//
// Fixed inputs are RFC 7748 §6.1's X25519 example keypairs (Alice=client, Bob=daemon),
// so client_pub/daemon_pub are themselves RFC known-answers.
const vectorsPath = "../../protocol/vectors/handshake.json"

const (
	rfcAlicePriv = "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	rfcAlicePub  = "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"
	rfcBobPriv   = "5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb"
	rfcBobPub    = "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"

	// katChallenge is the fixed stand-in for the daemon's random per-connection challenge.
	// It is SHA256("oculus/v1 vector challenge") — a derivable constant rather than typed
	// bytes, so anyone can regenerate the whole file from first principles and get the same
	// answer. It must never change: it is a known-answer, not a secret.
	katChallenge = "9d6065b0ce1c1ce9a794dc7f8e33e9800e4227c6628da21c97a8eac9363fb7ae"
)

type handshakeVectors struct {
	Description   string `json:"description"`
	ClientPriv    string `json:"client_priv"`
	DaemonPriv    string `json:"daemon_priv"`
	ClientPub     string `json:"client_pub"`
	DaemonPub     string `json:"daemon_pub"`
	C2DKey        string `json:"c2d_key"`
	D2CKey        string `json:"d2c_key"`
	SealPlaintext string `json:"seal_plaintext"`
	// OpenFrame is a nonce||ciphertext frame (fixed all-zero KAT nonce) that both
	// languages must OPEN to SealPlaintext under C2DKey. Production Seal uses a random
	// nonce, so the vector pins Open (deterministic), not Seal.
	OpenFrame string `json:"open_frame"`

	// v1 fields pin the challenge-bound schedule (see crypto.DeriveSessionKeysV1). Both
	// schedules are pinned at once because both are on the wire during the transition, and
	// a change that accidentally made them agree would be a silent loss of replay
	// protection — the vectors have to be able to see that.
	Challenge  string `json:"challenge"`
	Transcript string `json:"transcript"`
	V1C2DKey   string `json:"v1_c2d_key"`
	V1D2CKey   string `json:"v1_d2c_key"`
	// V1FramedPlaintext is seq(8, big-endian) || SealPlaintext: the exact byte string a v1
	// endpoint seals. Pinning it locks the sequence framing (width, endianness, position)
	// across the two languages, which is otherwise only asserted by prose.
	V1FramedPlaintext string `json:"v1_framed_plaintext"`
	V1OpenFrame       string `json:"v1_open_frame"`
}

// v1SeqLen mirrors transport.seqLen. Duplicated rather than imported because daemon/crypto
// must not depend on daemon/transport (transport depends on crypto); if these ever diverge
// the Swift side stops decoding, which is why the width is pinned in the vectors too.
const v1SeqLen = 8

func TestHandshakeGoldenVectors(t *testing.T) {
	clientPriv := mustHex(t, rfcAlicePriv)
	daemonPriv := mustHex(t, rfcBobPriv)

	client, err := KeyPairFromPrivate(clientPriv)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := KeyPairFromPrivate(daemonPriv)
	if err != nil {
		t.Fatal(err)
	}

	// The fixed pubkeys are RFC 7748 §6.1 known-answers.
	if hex.EncodeToString(client.Public()) != rfcAlicePub {
		t.Fatalf("client pub = %x, want RFC %s", client.Public(), rfcAlicePub)
	}
	if hex.EncodeToString(daemon.Public()) != rfcBobPub {
		t.Fatalf("daemon pub = %x, want RFC %s", daemon.Public(), rfcBobPub)
	}

	ck, err := DeriveSessionKeys(client, daemon.Public())
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("oculus handshake vector")

	// Build a deterministic KAT frame with a fixed all-zero nonce (production Seal is
	// random, so we pin Open, not Seal). Both languages must decrypt this frame.
	aead, err := chacha20poly1305.New(ck.C2D)
	if err != nil {
		t.Fatal(err)
	}
	katNonce := make([]byte, chacha20poly1305.NonceSize) // fixed all-zero KAT nonce
	frame := append(append([]byte{}, katNonce...), aead.Seal(nil, katNonce, plaintext, nil)...)

	// The production Opener must decrypt the KAT frame back to the plaintext.
	opener, err := NewOpener(ck.C2D)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := opener.Open(frame); err != nil || string(got) != string(plaintext) {
		t.Fatalf("Open(KAT frame) = %q, err %v; want %q", got, err, plaintext)
	}

	// --- v1: the same channel, bound to the daemon's challenge ---
	challenge := mustHex(t, katChallenge)
	v1, err := DeriveSessionKeysV1(client, daemon.Public(), client.Public(), daemon.Public(), challenge)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon derives from the mirror image (local=daemon, remote=client) and must land
	// on the same keys — that is the property the whole handshake rests on.
	v1d, err := DeriveSessionKeysV1(daemon, client.Public(), client.Public(), daemon.Public(), challenge)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v1.C2D, v1d.C2D) || !bytes.Equal(v1.D2C, v1d.D2C) {
		t.Fatal("client and daemon derived different v1 keys")
	}
	// A v1 key must never coincide with the v0 key for the same pair: if it did, a recorded
	// v0 frame would open on a v1 channel and the replay fix would be cosmetic.
	if bytes.Equal(v1.C2D, ck.C2D) || bytes.Equal(v1.D2C, ck.D2C) {
		t.Fatal("v1 keys equal v0 keys — the challenge is not reaching the derivation")
	}

	transcript, err := HandshakeTranscript(client.Public(), daemon.Public(), challenge)
	if err != nil {
		t.Fatal(err)
	}

	// A v1 endpoint seals seq||plaintext, so the KAT frame must too — that is what pins the
	// framing across languages.
	framed := make([]byte, v1SeqLen+len(plaintext))
	binary.BigEndian.PutUint64(framed[:v1SeqLen], 0)
	copy(framed[v1SeqLen:], plaintext)

	v1aead, err := chacha20poly1305.New(v1.C2D)
	if err != nil {
		t.Fatal(err)
	}
	v1frame := append(append([]byte{}, katNonce...), v1aead.Seal(nil, katNonce, framed, nil)...)
	v1opener, err := NewOpener(v1.C2D)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v1opener.Open(v1frame); err != nil || !bytes.Equal(got, framed) {
		t.Fatalf("Open(v1 KAT frame) = %x, err %v; want %x", got, err, framed)
	}

	got := handshakeVectors{
		Description:       "Oculus E2EE KAT: X25519 -> HKDF-SHA256 -> ChaCha20-Poly1305 (12-byte nonce, nonce-prefixed frame). v0 = static-static keys (legacy, replayable). v1 = keys bound to SHA256('oculus/v1 transcript'||clientPub||daemonPub||challenge), payload prefixed with an 8-byte big-endian sequence number. Production nonces are random; the open_frame fields are fixed all-zero-nonce KATs both sides must OPEN. Keys are RFC 7748 6.1; challenge is SHA256('oculus/v1 vector challenge').",
		ClientPriv:        rfcAlicePriv,
		DaemonPriv:        rfcBobPriv,
		ClientPub:         hex.EncodeToString(client.Public()),
		DaemonPub:         hex.EncodeToString(daemon.Public()),
		C2DKey:            hex.EncodeToString(ck.C2D),
		D2CKey:            hex.EncodeToString(ck.D2C),
		SealPlaintext:     hex.EncodeToString(plaintext),
		OpenFrame:         hex.EncodeToString(frame),
		Challenge:         katChallenge,
		Transcript:        hex.EncodeToString(transcript),
		V1C2DKey:          hex.EncodeToString(v1.C2D),
		V1D2CKey:          hex.EncodeToString(v1.D2C),
		V1FramedPlaintext: hex.EncodeToString(framed),
		V1OpenFrame:       hex.EncodeToString(v1frame),
	}

	if os.Getenv("OCULUS_UPDATE_VECTORS") == "1" {
		if err := os.MkdirAll(filepath.Dir(vectorsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(vectorsPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote golden vectors to %s", vectorsPath)
	}

	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read golden vectors (generate with OCULUS_UPDATE_VECTORS=1): %v", err)
	}
	var want handshakeVectors
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("implementation does not match golden vectors\n got:  %+v\n want: %+v", got, want)
	}
}
