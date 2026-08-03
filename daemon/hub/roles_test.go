package hub

import "testing"

// TestSoloUserIsNeverGated is the property that matters most for the default deployment: one person
// on their own machine must never acquire permission friction they didn't ask for.
func TestSoloUserIsNeverGated(t *testing.T) {
	r := newRoleRegistry()
	// Enforcement off (the default) → every connection is the owner, even an unknown one.
	if got := r.role(nil); got != RoleOwner {
		t.Fatalf("with enforcement off a client must be the owner, got %q", got)
	}
	for _, c := range []capability{capWatch, capSteer, capApprove, capOwner} {
		if !roleAllows(r.role(nil), c) {
			t.Errorf("solo user must retain capability %v", c)
		}
	}
}

// TestEnforcementDefaultsToObserver: once sharing is on, an unrecognized connection may only watch.
func TestEnforcementDefaultsToObserver(t *testing.T) {
	r := newRoleRegistry()
	r.SetEnabled(true)
	if got := r.role(nil); got != RoleObserver {
		t.Fatalf("with enforcement on an unknown client must be an observer, got %q", got)
	}
}

func TestRoleCapabilities(t *testing.T) {
	cases := []struct {
		role                         string
		watch, steer, approve, owner bool
	}{
		{RoleOwner, true, true, true, true},
		{RoleSteerer, true, true, false, false}, // may ask the agent to act, may NOT authorize it
		{RoleObserver, true, false, false, false},
	}
	for _, c := range cases {
		if roleAllows(c.role, capWatch) != c.watch {
			t.Errorf("%s watch = %v, want %v", c.role, !c.watch, c.watch)
		}
		if roleAllows(c.role, capSteer) != c.steer {
			t.Errorf("%s steer = %v, want %v", c.role, !c.steer, c.steer)
		}
		if roleAllows(c.role, capApprove) != c.approve {
			t.Errorf("%s approve = %v, want %v", c.role, !c.approve, c.approve)
		}
		if roleAllows(c.role, capOwner) != c.owner {
			t.Errorf("%s owner = %v, want %v", c.role, !c.owner, c.owner)
		}
	}
	// An unknown role string must never be treated as privileged.
	if roleAllows("admin", capSteer) || roleAllows("", capApprove) || roleAllows("admin", capOwner) {
		t.Error("an unrecognized role must carry no steering or approval capability")
	}
}

// TestSteererCannotApprove is the Cursor failure mode, guarded: a teammate may prompt the agent but
// may not authorize a tool that acts with the owner's credentials.
func TestSteererCannotApprove(t *testing.T) {
	if roleAllows(RoleSteerer, capApprove) {
		t.Fatal("a steerer must NOT be able to answer approvals — those act with the owner's credentials")
	}
	if !roleAllows(RoleSteerer, capSteer) {
		t.Fatal("a steerer must still be able to prompt")
	}
}

// TestRoleSurvivesToggle: turning enforcement off restores full access, on re-gates it.
func TestRoleToggle(t *testing.T) {
	r := newRoleRegistry()
	r.SetEnabled(true)
	if r.role(nil) != RoleObserver {
		t.Fatal("expected observer while enabled")
	}
	r.SetEnabled(false)
	if r.role(nil) != RoleOwner {
		t.Fatal("disabling enforcement must restore owner access")
	}
}
