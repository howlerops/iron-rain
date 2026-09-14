package hub

import (
	"testing"
	"time"
)

// TestInviteCarriesItsOwnCredential is the whole point: sharing must not mean handing over the
// owner's secret, which is owner-equivalent.
func TestInviteCarriesItsOwnCredential(t *testing.T) {
	h := New()
	accept := h.AcceptSecret("owner-secret")
	inv := h.invites.create("Sam", RoleObserver, time.Hour)

	if inv.Secret == "owner-secret" || inv.Secret == "" {
		t.Fatal("an invite must carry its own independent secret")
	}
	guest := []byte{1, 2, 3}
	if !accept(guest, inv.Secret) {
		t.Fatal("a live invite secret must authenticate")
	}
	if role := h.roleForConn(guest); role != RoleObserver {
		t.Fatalf("an invited guest should arrive as %s, got %s", RoleObserver, role)
	}
	// The owner's own device is unaffected.
	owner := []byte{9, 9, 9}
	if !accept(owner, "owner-secret") {
		t.Fatal("the owner's secret must still authenticate")
	}
	if role := h.roleForConn(owner); role != RoleOwner {
		t.Fatalf("the owner should be %s, got %s", RoleOwner, role)
	}
}

// TestInviteCannotMintAnOwner: escalation via a crafted role string must be impossible.
func TestInviteCannotMintAnOwner(t *testing.T) {
	h := New()
	for _, requested := range []string{RoleOwner, "admin", "", "OWNER"} {
		inv := h.invites.create("x", requested, time.Hour)
		if inv.Role == RoleOwner {
			t.Errorf("an invite requesting %q minted an OWNER — privilege escalation", requested)
		}
	}
	// A legitimate steerer invite is honored.
	if inv := h.invites.create("x", RoleSteerer, time.Hour); inv.Role != RoleSteerer {
		t.Errorf("a steerer invite should be honored, got %s", inv.Role)
	}
}

// TestInviteExpiryAndRevocation: a share link that works forever is a credential someone will paste
// into a chat log and forget about.
func TestInviteExpiryAndRevocation(t *testing.T) {
	h := New()
	accept := h.AcceptSecret("owner-secret")

	expired := h.invites.create("old", RoleObserver, time.Hour)
	expired.ExpiresAt = time.Now().Add(-time.Minute) // force lapse
	if accept([]byte{7}, expired.Secret) {
		t.Error("an expired invite must not authenticate")
	}

	live := h.invites.create("live", RoleObserver, time.Hour)
	guest := []byte{8}
	if !accept(guest, live.Secret) {
		t.Fatal("a live invite should authenticate")
	}
	if _, ok := h.invites.revoke(live.ID); !ok {
		t.Fatal("revoke should find the invite")
	}
	if accept([]byte{11}, live.Secret) {
		t.Error("a revoked invite must stop authenticating")
	}
	if _, ok := h.invites.roleFor(guest); ok {
		t.Error("revoking must un-link the clients that redeemed it")
	}
	// Listing prunes the lapsed one rather than showing a dead credential.
	for _, i := range h.inviteList().Invites {
		if i.ID == expired.ID {
			t.Error("an expired invite should not be listed")
		}
	}
}

// TestRedeemingAnInviteEnablesEnforcement: a guest must never arrive with owner powers just because
// nobody remembered to turn sharing on first.
func TestRedeemingAnInviteEnablesEnforcement(t *testing.T) {
	h := New()
	if h.roles.isEnabled() {
		t.Fatal("enforcement should start off")
	}
	inv := h.invites.create("Sam", RoleObserver, time.Hour)
	h.AcceptSecret("owner-secret")([]byte{4}, inv.Secret)
	if !h.roles.isEnabled() {
		t.Fatal("redeeming an invite must enable role enforcement")
	}
}

// TestWrongSecretIsRejected guards the obvious.
func TestWrongSecretIsRejected(t *testing.T) {
	h := New()
	accept := h.AcceptSecret("owner-secret")
	if accept([]byte{1}, "not-the-secret") {
		t.Fatal("an unknown credential must be rejected")
	}
	if accept([]byte{1}, "") {
		t.Fatal("an empty credential must be rejected")
	}
}

// A refused device must not burn the invite's seat.
//
// redeem() consumed the slot BEFORE the caller decided whether to admit the device, and that
// decision can say no: a revoked device presenting a valid invite is refused, correctly, since an
// invite must not undo a revocation. But the seat was already gone, so a one-use link read
// "Redeemed 1/1" for a device that never got in, and the owner had to mint a new link with nothing
// on screen explaining why the first was spent.
func TestARefusedDeviceDoesNotBurnTheInviteSeat(t *testing.T) {
	h := New()
	accept := h.AcceptSecret("owner-secret")
	inv := h.invites.create("Sam", RoleObserver, time.Hour) // one seat

	// A device the owner has revoked.
	revoked := []byte{4, 4, 4}
	if !h.enrollGuest(revoked) {
		t.Fatal("could not enrol the device to be revoked")
	}
	reg := h.deviceRegistry()
	reg.mu.Lock()
	reg.byID[hexKey(revoked)].Revoked = true
	reg.saveLocked()
	reg.mu.Unlock()

	if accept(revoked, inv.Secret) {
		t.Fatal("a revoked device authenticated with an invite — revocation must win")
	}

	// The seat must still be there for the person the link was actually for.
	guest := []byte{5, 5, 5}
	if !accept(guest, inv.Secret) {
		t.Fatal("the invite's only seat was consumed by a device that was refused.\n\n" +
			"The slot is taken before the enrolment decision, so a refused device permanently burns a " +
			"one-use link: the owner sees \"Redeemed 1/1\" for someone who never got in, and has to " +
			"mint another with nothing explaining why.")
	}
	if role := h.roleForConn(guest); role != RoleObserver {
		t.Fatalf("the guest arrived as %s, want %s", role, RoleObserver)
	}
}

// The seat must still be consumed by a device that IS admitted — releasing it on refusal must not
// turn a one-use link into an unlimited one.
func TestAnAdmittedDeviceStillConsumesTheSeat(t *testing.T) {
	h := New()
	accept := h.AcceptSecret("owner-secret")
	inv := h.invites.create("Sam", RoleObserver, time.Hour) // one seat

	if !accept([]byte{1, 1, 1}, inv.Secret) {
		t.Fatal("the first guest should be admitted")
	}
	if accept([]byte{2, 2, 2}, inv.Secret) {
		t.Fatal("a SECOND device got in on a one-seat invite — the link is now unlimited")
	}
	// The original device reconnecting is not a second seat.
	if !accept([]byte{1, 1, 1}, inv.Secret) {
		t.Error("the admitted guest could not reconnect — a dropped Wi-Fi connection must not cost a seat")
	}
	_ = inv
}
