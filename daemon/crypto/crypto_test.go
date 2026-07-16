package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// RFC 7748 §5.2, first X25519 test vector. Proves our X25519 is standard, which is
// what guarantees interop with Swift CryptoKit's Curve25519.
func TestX25519_RFC7748Vector(t *testing.T) {
	scalar := mustHex(t, "a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4")
	u := mustHex(t, "e6db6867583030db3594c1a424b15f7c726624ec26b3353b10a903a6d0ab1c4c")
	want := mustHex(t, "c3da55379de9c6908e94ea4df28d084f32eccf03491c71f754b4075577a28552")

	got, err := X25519(scalar, u)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("X25519 = %x, want %x", got, want)
	}
}

func TestGenerateKeyPair_PublicIs32Bytes(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if len(kp.Public()) != 32 {
		t.Fatalf("public key len = %d, want 32", len(kp.Public()))
	}
}

// The core interop property: both endpoints independently derive identical
// directional session keys from their own private key + the peer's public key.
func TestDeriveSessionKeys_BothSidesAgree(t *testing.T) {
	client, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	ck, err := DeriveSessionKeys(client, daemon.Public())
	if err != nil {
		t.Fatal(err)
	}
	dk, err := DeriveSessionKeys(daemon, client.Public())
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(ck.C2D, dk.C2D) || !bytes.Equal(ck.D2C, dk.D2C) {
		t.Fatal("client and daemon derived different session keys")
	}
	if bytes.Equal(ck.C2D, ck.D2C) {
		t.Fatal("directional keys (c2d, d2c) must differ")
	}
	if len(ck.C2D) != 32 || len(ck.D2C) != 32 {
		t.Fatalf("session keys must be 32 bytes, got %d/%d", len(ck.C2D), len(ck.D2C))
	}
}

func TestChannel_RoundTrip(t *testing.T) {
	client, _ := GenerateKeyPair()
	daemon, _ := GenerateKeyPair()
	ck, _ := DeriveSessionKeys(client, daemon.Public())
	dk, _ := DeriveSessionKeys(daemon, client.Public())

	// Client seals on the client->daemon key; daemon opens on the same key.
	sealer, err := NewSealer(ck.C2D)
	if err != nil {
		t.Fatal(err)
	}
	opener, err := NewOpener(dk.C2D)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("hello oculus")
	frame, err := sealer.Seal(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := opener.Open(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round-trip = %q, want %q", got, msg)
	}
}

func TestChannel_TamperDetected(t *testing.T) {
	client, _ := GenerateKeyPair()
	daemon, _ := GenerateKeyPair()
	ck, _ := DeriveSessionKeys(client, daemon.Public())
	dk, _ := DeriveSessionKeys(daemon, client.Public())
	sealer, _ := NewSealer(ck.C2D)
	opener, _ := NewOpener(dk.C2D)

	frame, _ := sealer.Seal([]byte("secret"))
	frame[len(frame)-1] ^= 0xff // flip a ciphertext byte
	if _, err := opener.Open(frame); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestChannel_NonceAdvances(t *testing.T) {
	client, _ := GenerateKeyPair()
	daemon, _ := GenerateKeyPair()
	ck, _ := DeriveSessionKeys(client, daemon.Public())
	sealer, _ := NewSealer(ck.C2D)

	a, _ := sealer.Seal([]byte("x"))
	b, _ := sealer.Seal([]byte("x"))
	if bytes.Equal(a, b) {
		t.Fatal("sealing the same plaintext twice must differ (nonce must advance)")
	}
}
