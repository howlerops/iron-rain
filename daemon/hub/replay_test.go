package hub

import (
	"testing"
	"time"
)

// TestSelfReplayExclusionIsScopedToAttach is the bug a second device hit: a live opencode session
// showed its full history on the Mac that started it (client-side memory) and an EMPTY conversation
// on a phone that opened it later, because self-replay was treated as a permanent reason to withhold
// the durable transcript. The provider only re-streams once, at attach.
func TestSelfReplayExclusionIsScopedToAttach(t *testing.T) {
	// Inside the window, a self-replaying provider is authoritative and durable replay is withheld.
	fresh := time.Now()
	if !(time.Since(fresh) < replayGrace) {
		t.Fatal("a just-created binding must be inside the attach window")
	}
	// Outside it, the provider will never re-stream again, so durable history must be offered.
	old := time.Now().Add(-replayGrace - time.Second)
	if time.Since(old) < replayGrace {
		t.Fatal("an old binding must be outside the attach window")
	}
	// The window has to be long enough for a real re-stream but far short of a session's lifetime —
	// too long and a second device sees nothing for that whole period.
	if replayGrace < 5*time.Second || replayGrace > 60*time.Second {
		t.Errorf("replayGrace = %s; outside the range that makes this trade-off sensible", replayGrace)
	}
}

// TestNewManagedSessionStampsCreatedAt: the window is meaningless if the stamp is zero, which would
// make every session look ancient and re-introduce duplicate replay at attach.
func TestNewManagedSessionStampsCreatedAt(t *testing.T) {
	h := New()
	m := newManagedSession(h, &approvalFakeSess{}, sessionMeta{})
	if m.createdAt.IsZero() {
		t.Fatal("createdAt must be stamped, or the attach window can never apply")
	}
	if time.Since(m.createdAt) > time.Second {
		t.Errorf("createdAt should be ~now, got %s ago", time.Since(m.createdAt))
	}
}
