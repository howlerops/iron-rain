package hub

import (
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/transport"
)

// What a watch-only guest may actually do, asserted against the capability model rather than the
// source text.
//
// The census test next door proves every message type has MADE a decision. This one proves the
// decisions are the ones intended — it is the difference between "someone wrote a gate here" and
// "the gate keeps the right people out".
//
// The solo case is the reason all of this is affordable, and it is checked first: with sharing off,
// which is the default and the only state a single-user install is ever in, every one of these
// passes unconditionally.
func TestWhatEachRoleMayDo(t *testing.T) {
	cases := []struct {
		what                     string
		cap                      capability
		observer, steerer, owner bool
	}{
		// The editor read family. capSteer, matching fs.read/fs.tree — it is the same access to the
		// same files through a different door, and lsp.rename beside it was already capSteer.
		{"use the code editor (lsp.open/hover/definition/…)", editorReadCap, false, true, true},
		// Mutates a feed every device reads, including the owner's needs-you inbox.
		{"mark activity read", capSteer, false, true, true},
		// The owner's own configuration and infrastructure. None of it is session content.
		{"list enrolled devices", capOwner, false, false, true},
		{"list provider accounts", capOwner, false, false, true},
		{"read an account's quota", capOwner, false, false, true},
		{"list remote hosts", capOwner, false, false, true},
		{"check a remote host", capOwner, false, false, true},
		{"see notification settings", capOwner, false, false, true},
		{"change notification settings", capOwner, false, false, true},
		{"browse folders", capOwner, false, false, true},
		// Deliberately left open: a watcher can already read the session's transcript, and the diff
		// is the same work by another route.
		{"read a session's diff", capWatch, true, true, true},
	}

	for _, tc := range cases {
		if got := roleAllows(RoleObserver, tc.cap); got != tc.observer {
			t.Errorf("observer may %s = %v, want %v", tc.what, got, tc.observer)
		}
		if got := roleAllows(RoleSteerer, tc.cap); got != tc.steerer {
			t.Errorf("steerer may %s = %v, want %v", tc.what, got, tc.steerer)
		}
		if got := roleAllows(RoleOwner, tc.cap); got != tc.owner {
			t.Errorf("owner may %s = %v, want %v", tc.what, got, tc.owner)
		}
	}
}

// The promise that makes every gate above free: a solo user is never gated by any of them.
//
// Sharing is off by default, and with it off roleRegistry.role() returns owner for every connection
// — so nothing added here can introduce friction for someone running this on their own machine. If
// that ever stops being true, this whole design changes and this test is where it surfaces.
func TestASoloUserPassesEveryGateAdded(t *testing.T) {
	h := &Hub{roles: newRoleRegistry()} // enforcement off, as shipped
	conn := &transport.Conn{}
	for _, c := range []capability{capWatch, capSteer, capApprove, capOwner} {
		if !roleAllows(h.roles.role(conn), c) {
			t.Fatalf("a solo user was refused capability %v — every gate in the dispatch switch is "+
				"predicated on this being impossible", c)
		}
	}
}

// A refusal has to say what was refused and what to do about it. These messages are the only place
// the boundary is ever explained: the client renders its controls from a static layout, so a button
// that fails with no reason reads as a broken feature, and the honest guess from the other side is
// that the app is buggy rather than that the limit is deliberate.
func TestARefusalExplainsItself(t *testing.T) {
	h := &Hub{roles: newRoleRegistry(), clients: map[*transport.Conn]*hubClient{}}
	h.roles.SetEnabled(true)

	// The editor gate carries a reason, because being able to read a transcript and not hover a
	// symbol is not self-evident.
	msg := refusalFor(t, editorReadCap, "use the code editor",
		"Reading the project's files is the same access as the file browser, which is limited to people who can steer.")
	if !strings.Contains(msg, "watching this session") {
		t.Errorf("a steer-level refusal must say the reader is a watcher, got %q", msg)
	}
	if !strings.Contains(msg, "file browser") {
		t.Errorf("the editor refusal dropped its reason, got %q", msg)
	}

	// An owner-level refusal names the action rather than reciting the rule.
	msg = refusalFor(t, capOwner, "list enrolled devices", "")
	if !strings.Contains(msg, "list enrolled devices") {
		t.Errorf("an owner-only refusal must name what was refused, got %q", msg)
	}
}

// refusalFor asks the PRODUCTION renderer what it would say. Re-deriving the string here would make
// this a test of its own copy.
func refusalFor(t *testing.T, c capability, what, because string) string {
	t.Helper()
	return refusalMessage(c, what, because)
}
