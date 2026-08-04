package hub

import (
	"path/filepath"
	"testing"
)

// TestDeviceEnrollmentAndRevocation closes the one-permanent-shared-secret hole.
//
// Every paired device presenting the SAME secret means there is no way to answer "which devices can
// reach my machine?" and no way to revoke one — a lost phone means rotating the secret and re-pairing
// everything. Enrolling the static public key each client presents in the handshake makes both
// questions answerable; minting that device its own credential (credentials_test.go) is what makes
// the answer enforceable.
func TestDeviceEnrollmentAndRevocation(t *testing.T) {
	h := New()
	h.SetDevicesPath(filepath.Join(t.TempDir(), "devices.json"))
	auth := h.AcceptSecret("s3cret")

	phone := []byte{1, 2, 3}
	laptop := []byte{4, 5, 6}

	if !auth(phone, "s3cret") || !auth(laptop, "s3cret") {
		t.Fatal("a correct secret must pair")
	}
	if got := len(h.Devices()); got != 2 {
		t.Fatalf("enrolled %d devices, want 2 — pairing must record WHICH device paired", got)
	}

	// Revoking one device must lock out exactly that device, even though it still knows the secret.
	if err := h.RevokeDevice(hexKey(phone)); err != nil {
		t.Fatal(err)
	}
	if auth(phone, "s3cret") {
		t.Error("a revoked device must be refused even with the right secret — otherwise revocation means nothing")
	}
	if !auth(laptop, "s3cret") {
		t.Error("revoking one device must not lock out the others")
	}
	if got := len(h.Devices()); got != 1 {
		t.Errorf("device list has %d entries after revoking one, want 1", got)
	}
}

// Enrollment must survive a daemon restart, or revocation is undone by the next update.
func TestDeviceEnrollmentPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	h := New()
	h.SetDevicesPath(path)
	auth := h.AcceptSecret("k")
	auth([]byte{9, 9}, "k")
	_ = h.RevokeDevice(hexKey([]byte{9, 9}))

	h2 := New()
	h2.SetDevicesPath(path)
	if h2.AcceptSecret("k")([]byte{9, 9}, "k") {
		t.Error("a revocation must survive a restart — the daemon self-updates every six hours")
	}
}

// A wrong secret never enrolls anything.
func TestWrongSecretDoesNotEnroll(t *testing.T) {
	h := New()
	h.SetDevicesPath(filepath.Join(t.TempDir(), "d.json"))
	if h.AcceptSecret("right")([]byte{7}, "wrong") {
		t.Fatal("wrong secret must be refused")
	}
	if len(h.Devices()) != 0 {
		t.Error("a failed pairing must not appear in the device list")
	}
}
