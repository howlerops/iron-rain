package crypto

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
)

type handshakeVectors struct {
	Description       string `json:"description"`
	ClientPriv        string `json:"client_priv"`
	DaemonPriv        string `json:"daemon_priv"`
	ClientPub         string `json:"client_pub"`
	DaemonPub         string `json:"daemon_pub"`
	C2DKey            string `json:"c2d_key"`
	D2CKey            string `json:"d2c_key"`
	SealPlaintext     string `json:"seal_plaintext"`
	SealCounter0Frame string `json:"seal_counter0_frame"`
}

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
	sealer, err := NewSealer(ck.C2D)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := sealer.Seal(plaintext) // counter 0 -> deterministic nonce
	if err != nil {
		t.Fatal(err)
	}

	got := handshakeVectors{
		Description:       "Oculus v0 E2EE KAT: X25519 static-static -> HKDF-SHA256 -> ChaCha20-Poly1305 (12-byte counter nonce, nonce-prefixed frame). Keys are RFC 7748 6.1. Swift CryptoKit must reproduce every field.",
		ClientPriv:        rfcAlicePriv,
		DaemonPriv:        rfcBobPriv,
		ClientPub:         hex.EncodeToString(client.Public()),
		DaemonPub:         hex.EncodeToString(daemon.Public()),
		C2DKey:            hex.EncodeToString(ck.C2D),
		D2CKey:            hex.EncodeToString(ck.D2C),
		SealPlaintext:     hex.EncodeToString(plaintext),
		SealCounter0Frame: hex.EncodeToString(frame),
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
