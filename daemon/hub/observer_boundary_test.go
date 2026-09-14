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
//
// NOTE what this does and does not cover. It asserts the RENDERER's shape — that a steer-level
// refusal names the reader as a watcher, and an owner-level one names the action. It deliberately no
// longer tries to prove that the editor gate supplies a reason, because it cannot: passing the
// reason in as a literal and then asserting it comes back only proves that refusalMessage appends
// its argument. Stripping the reason from requireEditorRead left the old version green. That claim
// is now made where it can actually be checked — refusal_wire_test.go demotes a real connection and
// reads the bytes the daemon sends.
func TestARefusalExplainsItself(t *testing.T) {
	// A steer-level refusal tells the reader what they ARE, because the action name alone ("use the
	// code editor") does not explain why they of all people cannot.
	msg := refusalMessage(editorReadCap, "use the code editor", "")
	if !strings.Contains(msg, "watching this session") {
		t.Errorf("a steer-level refusal must say the reader is a watcher, got %q", msg)
	}

	// An owner-level refusal names the ACTION rather than reciting the rule.
	msg = refusalMessage(capOwner, "list enrolled devices", "")
	if !strings.Contains(msg, "list enrolled devices") {
		t.Errorf("an owner-only refusal must name what was refused, got %q", msg)
	}

	// And a supplied reason is appended rather than replacing the sentence.
	msg = refusalMessage(capOwner, "revoke a device", "Devices belong to whoever owns this Mac.")
	if !strings.Contains(msg, "revoke a device") || !strings.Contains(msg, "belong to whoever owns") {
		t.Errorf("a reason must be added to the refusal, not instead of it, got %q", msg)
	}
}
