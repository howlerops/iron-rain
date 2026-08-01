package wake

import (
	"testing"
)

// TestGuardRefcounts: several sessions can be working at once, and the machine must stay awake until
// the LAST of them finishes. Releasing on the first completion would put the Mac to sleep with other
// agents still running — which on a phone reads as every remaining session dying at once.
func TestGuardRefcounts(t *testing.T) {
	var starts, stops int
	g := &Guard{
		start: func() error { starts++; return nil },
		stop:  func() { stops++ },
	}

	g.Hold()
	g.Hold()
	if starts != 1 {
		t.Fatalf("start called %d times, want 1 — a second hold must not spawn a second assertion", starts)
	}
	g.Release()
	if stops != 0 {
		t.Fatalf("released while work remains: stop called %d times, want 0", stops)
	}
	g.Release()
	if stops != 1 {
		t.Fatalf("stop called %d times after the last release, want 1", stops)
	}

	// A later hold must be able to re-acquire.
	g.Hold()
	if starts != 2 {
		t.Errorf("start called %d times, want 2 — the guard must be reusable", starts)
	}
}

// Release without a matching Hold must not drive the count negative, or a later Hold would fail to
// start the assertion and the Mac would sleep mid-turn with nothing to show for it.
func TestUnbalancedReleaseIsSafe(t *testing.T) {
	var starts int
	g := &Guard{start: func() error { starts++; return nil }, stop: func() {}}
	g.Release()
	g.Release()
	g.Hold()
	if starts != 1 {
		t.Errorf("start called %d times, want 1 — stray releases must not corrupt the count", starts)
	}
}

// If the assertion cannot be taken, the daemon must carry on. Keeping a machine awake is a courtesy;
// failing to start a turn because of it would be absurd.
func TestFailureToAssertIsNotFatal(t *testing.T) {
	g := &Guard{start: func() error { return errFake }, stop: func() {}}
	g.Hold() // must not panic
	g.Release()
}
