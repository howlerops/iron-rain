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
	if !h.invites.revoke(live.ID) {
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
