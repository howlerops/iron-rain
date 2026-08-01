package hub

import (
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestDeriveStateIdleTurnEndedIsNotWorking is the heartbeat's own lie: the "done" branch demanded
// total > 0, so a session that finished its turn without ever writing a to-do list fell through
// every branch and came back "working" — forever, for a session that is plainly sitting idle. Most
// chat sessions have no to-dos at all, so this was the common case, not the corner one.
func TestDeriveStateIdleTurnEndedIsNotWorking(t *testing.T) {
	now := time.Now()
	idle := now.Add(-90 * time.Second) // well past activeWindow

	if got := deriveState(&managedSession{lastActivity: idle, turnEnded: true}, now); got == hbWorking {
		t.Fatalf("deriveState = %q for an idle session that ended its turn — the chip claims it is working", got)
	} else if got != hbDone {
		t.Fatalf("deriveState = %q, want %q", got, hbDone)
	}
	// Seeded-idle attach (no turn ever ran here, no to-dos) must be honest too.
	if got := deriveState(&managedSession{lastActivity: idle, lastStatus: protocol.StatusIdle}, now); got != hbDone {
		t.Fatalf("attached idle session derives %q, want %q", got, hbDone)
	}
	// A session that has NOT ended a turn still gets the benefit of the doubt (it may be mid-tool
	// with no events); only that path may return "working" while quiet.
	if got := deriveState(&managedSession{lastActivity: idle}, now); got != hbWorking {
		t.Fatalf("mid-turn quiet session derives %q, want %q (never call a live turn done)", got, hbWorking)
	}
}
