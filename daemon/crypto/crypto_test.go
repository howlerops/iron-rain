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

// The v1 property the replay fix rests on: both ends still agree, and a different
// challenge produces an unrelated channel.
func TestDeriveSessionKeysV1_BothSidesAgreeAndChallengeSeparatesSessions(t *testing.T) {
	client, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if len(challenge) != ChallengeSize {
		t.Fatalf("challenge len = %d, want %d", len(challenge), ChallengeSize)
	}

	// Client: local=client, remote=daemon. Daemon: the mirror image. Same keys.
	ck, err := DeriveSessionKeysV1(client, daemon.Public(), client.Public(), daemon.Public(), challenge)
	if err != nil {
		t.Fatal(err)
	}
	dk, err := DeriveSessionKeysV1(daemon, client.Public(), client.Public(), daemon.Public(), challenge)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ck.C2D, dk.C2D) || !bytes.Equal(ck.D2C, dk.D2C) {
		t.Fatal("client and daemon derived different v1 session keys")
	}
	if bytes.Equal(ck.C2D, ck.D2C) {
		t.Fatal("directional keys (c2d, d2c) must differ")
	}

	// The next connection's challenge must produce a channel that cannot open the last
	// one's frames — this is what makes a recorded stream worthless.
	next, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	nk, err := DeriveSessionKeysV1(client, daemon.Public(), client.Public(), daemon.Public(), next)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ck.C2D, nk.C2D) || bytes.Equal(ck.D2C, nk.D2C) {
		t.Fatal("a fresh challenge produced the same keys — the challenge is not reaching the derivation")
	}
	sealer, err := NewSealer(ck.C2D)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := sealer.Seal([]byte("recorded command"))
	if err != nil {
		t.Fatal(err)
	}
	opener, err := NewOpener(nk.C2D)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opener.Open(frame); err == nil {
		t.Fatal("a frame from the previous session opened under the new session's key")
	}

	// And v1 must never coincide with v0 for the same pair.
	v0, err := DeriveSessionKeys(client, daemon.Public())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(v0.C2D, ck.C2D) || bytes.Equal(v0.D2C, ck.D2C) {
		t.Fatal("v1 keys equal v0 keys")
	}
}

// Every transcript input is a fixed 32 bytes so the concatenation is unambiguous; short
// input is rejected rather than hashed, which is what keeps two different handshakes from
// producing the same transcript.
func TestHandshakeTranscript_RejectsWrongLengths(t *testing.T) {
	ok := bytes.Repeat([]byte{0xAB}, 32)
	if _, err := HandshakeTranscript(ok[:31], ok, ok); err == nil {
		t.Fatal("short client public key must be rejected")
	}
	if _, err := HandshakeTranscript(ok, ok[:16], ok); err == nil {
		t.Fatal("short daemon public key must be rejected")
	}
	if _, err := HandshakeTranscript(ok, ok, ok[:8]); err == nil {
		t.Fatal("short challenge must be rejected")
	}
	got, err := HandshakeTranscript(ok, ok, ok)
	if err != nil || len(got) != 32 {
		t.Fatalf("transcript = %d bytes, err %v; want 32", len(got), err)
	}
}

// Two calls must not return the same challenge.
func TestGenerateChallenge_IsFresh(t *testing.T) {
	a, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("GenerateChallenge returned the same bytes twice")
	}
}
