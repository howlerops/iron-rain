package hub

import (
	"path/filepath"
	"testing"
)

// A demotion must outlive the socket it was made on.
//
// The role registry is keyed on the live *transport.Conn and dropClient forgets it, so a demotion
// used to last exactly as long as the connection. A CREDENTIALED device — paired with a pairing
// code, so not a guest and not claimed by any invite — then fell through roleForConn to RoleOwner on
// its next connection: answering approvals, run.test (arbitrary `sh -c`), revoking the owner's own
// devices, minting pairing codes. A Wi-Fi blip was enough, the Sharing sheet re-rendered them as
// "owner", and nothing logged the change.
func TestADemotionSurvivesAReconnect(t *testing.T) {
	h := New()
	h.SetDevicesPath(filepath.Join(t.TempDir(), "devices.json"))
	pub := []byte{0xaa, 0x11, 0x22, 0x33}

	// A credentialed, non-guest device: the case that resolved back to owner.
	h.enroll(pub) // a credentialed, non-guest device
	if got := h.roleForConn(pub); got != RoleOwner {
		t.Fatalf("precondition: a credentialed device should start as %s, got %s", RoleOwner, got)
	}

	h.setDeviceRole(pub, RoleObserver)

	if got := h.roleForConn(pub); got != RoleObserver {
		t.Fatalf("after a demotion the device resolves as %s — it silently regained owner powers "+
			"on reconnect: approvals, run.test, device revocation, minting pairing codes", got)
	}
}

// The stored decision must survive a daemon restart too — it lives in devices.json.
func TestADemotionSurvivesADaemonRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	pub := []byte{0xbb, 0x44, 0x55, 0x66}

	h := New()
	h.SetDevicesPath(path)
	h.enroll(pub) // a credentialed, non-guest device
	h.setDeviceRole(pub, RoleObserver)

	// A fresh daemon reading the same file.
	h2 := New()
	h2.SetDevicesPath(path)
	if got := h2.roleForConn(pub); got != RoleObserver {
		t.Fatalf("the demotion did not survive a restart: got %s, want %s", got, RoleObserver)
	}
}

// An owner's own device, never demoted, must be unaffected.
func TestAnUndemotedDeviceIsStillAnOwner(t *testing.T) {
	h := New()
	h.SetDevicesPath(filepath.Join(t.TempDir(), "devices.json"))
	pub := []byte{0xcc, 0x77}
	h.enroll(pub)
	if got := h.roleForConn(pub); got != RoleOwner {
		t.Fatalf("an undemoted device resolved as %s — the stored-role check must fall through "+
			"when there is no stored decision", got)
	}
}
