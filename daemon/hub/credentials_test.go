package hub

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/transport"
)

// --- a message pipe, so a test can hold a REAL authenticated connection and watch it die ---

type credPipe struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
}

func newCredPipePair() (*credPipe, *credPipe) {
	a2b, b2a := make(chan []byte, 32), make(chan []byte, 32)
	return &credPipe{in: b2a, out: a2b, closed: make(chan struct{})},
		&credPipe{in: a2b, out: b2a, closed: make(chan struct{})}
}

func (p *credPipe) WriteMsg(b []byte) error {
	cp := append([]byte(nil), b...)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return fmt.Errorf("closed")
	}
}

func (p *credPipe) ReadMsg() ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-p.closed:
		return nil, fmt.Errorf("closed")
	}
}

func (p *credPipe) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}

func newHubWithPaths(t *testing.T) *Hub {
	t.Helper()
	dir := t.TempDir()
	h := New()
	h.SetDevicesPath(filepath.Join(dir, "devices.json"))
	h.SetCredentialsPath(filepath.Join(dir, "credentials.json"))
	return h
}

// TestPairingCodeIsSingleUse is the change that makes every leak path expire.
//
// The pairing credential travels in a URL and a QR: a screenshot, a synced clipboard, a photo of
// someone's screen. Permanent, it was a permanent shell on the owner's Mac. Spent, it is a dead
// string — which is what the second half of this test asserts.
func TestPairingCodeIsSingleUse(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")

	code, _ := h.MintPairCode(0)
	if code == "" {
		t.Fatal("MintPairCode returned nothing")
	}
	phone := []byte{1, 2, 3}
	if !accept(phone, code) {
		t.Fatal("a fresh pairing code must enroll a device")
	}
	// The same code again — the screenshot, the second scan, a replayed frame.
	if accept([]byte{4, 5, 6}, code) {
		t.Fatal("a spent pairing code must be refused: this is what makes a leaked QR harmless")
	}
	// Even from the device that legitimately used it: it has its own credential now.
	if accept(phone, code) {
		t.Fatal("a spent pairing code must be refused even for the device that spent it")
	}
}

// TestPairingCodeExpires: a code nobody used must still stop being a credential.
func TestPairingCodeExpires(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")

	code, expires := h.MintPairCode(20 * time.Millisecond)
	if !expires.After(time.Now()) {
		t.Fatal("a minted code should expire in the future")
	}
	time.Sleep(40 * time.Millisecond)
	if accept([]byte{1}, code) {
		t.Fatal("an expired pairing code must be refused")
	}
	if len(h.Devices()) != 0 {
		t.Fatal("an expired code must not enroll anything")
	}
}

// TestEnrollmentIssuesADistinctCredential: after pairing, the device authenticates with something
// that is NOT the pairing credential. That is the whole point of separating the two — the fragile,
// widely-copied one gets to be short-lived because it is not the long-term key.
func TestEnrollmentIssuesADistinctCredential(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")

	code, _ := h.MintPairCode(0)
	phone := []byte{1, 2, 3}
	if !accept(phone, code) {
		t.Fatal("pairing code should enroll")
	}
	cred, ok := h.creds().takePending(hexKey(phone))
	if !ok || cred == "" {
		t.Fatal("enrollment must mint a per-device credential for delivery")
	}
	if cred == code {
		t.Fatal("the device credential must not be the pairing code")
	}
	if !accept(phone, cred) {
		t.Fatal("a device must be able to reconnect with its own credential")
	}
	// Bound to the device: lifting the string alone gets you nothing.
	if accept([]byte{9, 9, 9}, cred) {
		t.Fatal("one device's credential must not authenticate another device")
	}
	// And it is not stored in the clear.
	reg := h.deviceRegistry()
	reg.mu.Lock()
	stored := reg.byID[hexKey(phone)].CredHash
	reg.mu.Unlock()
	if stored == "" || stored == cred {
		t.Fatal("the device registry must store a hash, not the credential itself")
	}
}

// TestRevokedDeviceIsDisconnectedAndCannotReturn is the finding this whole change exists for.
//
// "Revoke device" used to set a bool and rebroadcast a list. The stolen phone stayed connected, kept
// its role, and kept driving the agent until it chose to leave — and on its next launch it presented
// a brand-new keypair and was enrolled as a stranger. Both halves are asserted here: the live socket
// dies, and the credential is dead.
func TestRevokedDeviceIsDisconnectedAndCannotReturn(t *testing.T) {
	h := newHubWithPaths(t)
	daemonKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	clientKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	accept := h.AcceptSecret("")
	code, _ := h.MintPairCode(0)

	cPipe, sPipe := newCredPipePair()
	served := make(chan struct{})
	go func() {
		conn, err := transport.ServerHandshake(sPipe, daemonKP, accept)
		if err != nil {
			close(served)
			return
		}
		_ = h.Serve(context.Background(), conn)
		close(served)
	}()
	client, err := transport.ClientHandshake(cPipe, clientKP, daemonKP.Public(), code)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer client.Close()

	// Wait for the hub to register the connection before revoking, or we'd be testing a race.
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.clients)
		h.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the client never registered with the hub")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := h.RevokeDevice(hexKey(clientKP.Public())); err != nil {
		t.Fatal(err)
	}

	// The LIVE connection must go away on its own — not on the revoked device's next attempt.
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("revoking a device must close its live connection, not just refuse the next one")
	}

	// And it cannot come back with the credential it holds.
	cred, _ := h.creds().takePending(hexKey(clientKP.Public()))
	if cred != "" && accept(clientKP.Public(), cred) {
		t.Fatal("a revoked device's credential must stop working")
	}
	if accept(clientKP.Public(), code) {
		t.Fatal("a revoked device must not get back in by replaying the pairing code")
	}
}

// TestLegacySecretMigratesAnExistingPairing: nobody may be locked out by the upgrade.
//
// An already-paired device knows exactly one thing — the old permanent secret. It has to keep working
// long enough to be handed a credential of its own, and then the permanent secret has to die. Both
// ends of that are asserted here.
func TestLegacySecretMigratesAnExistingPairing(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("old-permanent-secret")

	phone := []byte{1, 2, 3}
	if !accept(phone, "old-permanent-secret") {
		t.Fatal("an already-paired device must still connect after the upgrade")
	}
	cred, ok := h.creds().takePending(hexKey(phone))
	if !ok || cred == "" {
		t.Fatal("presenting the old secret must mint a per-device credential")
	}
	if !accept(phone, cred) {
		t.Fatal("the migrated device must authenticate with its new credential")
	}

	// The device confirms it stored the credential; that starts the retirement clock.
	h.creds().noteMigrated()
	live, retireAt := h.LegacySecretStatus()
	if !live || retireAt == 0 {
		t.Fatal("the old secret should still work during the grace window, with a deadline set")
	}
	if accept([]byte{7, 7}, "old-permanent-secret") {
		// A second device that hasn't been updated yet is still admitted during the window — but it
		// must be a real device enrollment, not a bypass.
		if len(h.Devices()) < 2 {
			t.Fatal("a device admitted by the legacy secret must be enrolled")
		}
	}

	// Once retired, the permanent secret is not a credential any more — but the migrated device is
	// unaffected, which is the property that makes retiring it safe.
	h.RetireLegacySecret()
	if accept([]byte{8, 8}, "old-permanent-secret") {
		t.Fatal("a retired permanent secret must be refused")
	}
	if !accept(phone, cred) {
		t.Fatal("retiring the old secret must not lock out a device that already migrated")
	}
	if live, _ := h.LegacySecretStatus(); live {
		t.Fatal("LegacySecretStatus must report the secret as dead after retirement")
	}
}

// TestRetirementSurvivesRestart: the daemon self-updates and restarts often. A retirement that a
// restart undoes is not a retirement.
func TestRetirementSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.json")

	h := New()
	h.SetDevicesPath(filepath.Join(dir, "devices.json"))
	h.SetCredentialsPath(credPath)
	accept := h.AcceptSecret("old-permanent-secret")
	if !accept([]byte{1}, "old-permanent-secret") {
		t.Fatal("legacy secret should be accepted before retirement")
	}
	h.RetireLegacySecret()

	h2 := New()
	h2.SetDevicesPath(filepath.Join(dir, "devices.json"))
	h2.SetCredentialsPath(credPath)
	if h2.AcceptSecret("old-permanent-secret")([]byte{2}, "old-permanent-secret") {
		t.Fatal("a retired permanent secret must stay retired across a restart")
	}
}

// TestConfiguredSecretIsNeverRetired: --secret is a deliberate, scripted credential. Expiring it out
// from under an operator would break their setup on the next restart, and that call is theirs.
func TestConfiguredSecretIsNeverRetired(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptConfiguredSecret("scripted")
	if !accept([]byte{1}, "scripted") {
		t.Fatal("an explicitly configured secret must authenticate")
	}
	h.creds().noteMigrated()
	h.RetireLegacySecret()
	if !accept([]byte{2}, "scripted") {
		t.Fatal("an explicitly configured secret must not be retired by the daemon")
	}
}

// TestFreshInstallHasNoPermanentSecret: a machine with nothing to migrate must never acquire a
// permanent owner-equivalent credential.
func TestFreshInstallHasNoPermanentSecret(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")
	if accept([]byte{1}, "") {
		t.Fatal("an empty credential must be refused")
	}
	if live, _ := h.LegacySecretStatus(); live {
		t.Fatal("a fresh install must report no permanent secret at all")
	}
}

// TestInviteGuestGetsNoCredentialAndIsRevocable: a guest's access has to end when the invite does.
//
// If a guest were handed a per-device credential, an expired or revoked invite would leave behind a
// device that re-authenticates on its own — and roleForConn would then have no invite to read a role
// from, which is the shape of a silent promotion to owner.
func TestInviteGuestGetsNoCredentialAndIsRevocable(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")
	inv := h.invites.create("Sam", RoleObserver, time.Hour)

	guest := []byte{5, 5, 5}
	if !accept(guest, inv.Secret) {
		t.Fatal("a live invite must authenticate")
	}
	if _, ok := h.creds().takePending(hexKey(guest)); ok {
		t.Fatal("an invited guest must not be issued a per-device credential")
	}
	if !h.isGuestDevice(guest) {
		t.Fatal("an invited guest must be enrolled, or it cannot be revoked")
	}

	// Revoking the invite ends their access entirely.
	if _, ok := h.invites.revoke(inv.ID); !ok {
		t.Fatal("revoke should find the invite")
	}
	if accept(guest, inv.Secret) {
		t.Fatal("a revoked invite must stop authenticating")
	}
	if role := h.roleForConn(guest); role == RoleOwner {
		t.Fatal("a guest whose invite is gone must never resolve to owner")
	}
}

// TestInviteIsSingleDeviceByDefault: a share link gets pasted into chats, and chats forward.
func TestInviteIsSingleDeviceByDefault(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")
	inv := h.invites.create("Sam", RoleObserver, time.Hour)

	if !accept([]byte{1}, inv.Secret) {
		t.Fatal("the first device must be admitted")
	}
	if accept([]byte{2}, inv.Secret) {
		t.Fatal("a second device must be refused unless the owner asked for a multi-device invite")
	}
	// A reconnect from the device that already redeemed it is not a second device.
	if !accept([]byte{1}, inv.Secret) {
		t.Fatal("the redeeming device must be able to reconnect")
	}

	multi := h.invites.createFor("Team", RoleObserver, time.Hour, 3)
	if !accept([]byte{3}, multi.Secret) || !accept([]byte{4}, multi.Secret) {
		t.Fatal("an explicit multi-device invite must admit more than one")
	}
}

// TestExpiredInviteSweepReportsItsGuests: expiry has to end the SESSION, not just the link. The role
// is resolved once at connect, so a guest whose invite lapsed mid-session keeps it until something
// closes the socket — which is what the returned keys are for.
func TestExpiredInviteSweepReportsItsGuests(t *testing.T) {
	h := newHubWithPaths(t)
	accept := h.AcceptSecret("")
	inv := h.invites.create("Sam", RoleObserver, time.Hour)
	guest := []byte{6, 6}
	if !accept(guest, inv.Secret) {
		t.Fatal("invite should authenticate")
	}
	inv.ExpiresAt = time.Now().Add(-time.Minute) // force lapse

	pubs := h.invites.sweepExpired()
	if len(pubs) != 1 || pubs[0] != hexKey(guest) {
		t.Fatalf("the sweep must report the guests to disconnect, got %v", pubs)
	}
	if _, ok := h.invites.roleFor(guest); ok {
		t.Fatal("an expired invite must stop conferring a role")
	}
}
