package hub

import (
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/push"
)

// One budget stop, one notification.
//
// The budget branch calls closeTurn(NeedsYou, …) — which notifies through publishVerdict — and then
// pushed again directly. The user's phone buzzed twice for one event, with two different wordings of
// the same fact, which is how a notification category earns itself being switched off.
//
// Driven through heartbeatTick rather than closeTurn alone: the duplicate lives in the heartbeat's
// branch, so a test that calls closeTurn by hand would pass with the defect fully intact.
func TestBudgetStopNotifiesOnce(t *testing.T) {
	got := make(chan push.Notification, 8)
	h := New()
	h.notifier = &prRecordingNotifier{got: got}
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	m := newManagedSession(h, &prFakeSess{ch: make(chan agent.Event)}, sessionMeta{})
	m.mu.Lock()
	m.autonomous = true
	m.budgetUSD = 1.00
	m.costUSD = 1.25 // over budget
	m.lastActivity = time.Now()
	m.mu.Unlock()
	m.openTurn("")

	h.mu.Lock()
	h.sessions[m.sess.ID()] = m
	h.mu.Unlock()

	h.heartbeatTick()

	first, ok := nextPush(t, got)
	if !ok {
		t.Fatal("the budget stop pushed nothing — the user is never told their agent was stopped")
	}
	if first.Category != "AGENT_STALLED" {
		t.Errorf("category = %q, want AGENT_STALLED", first.Category)
	}

	select {
	case second := <-got:
		t.Fatalf("a second push for one budget stop: %q / %q (the first said %q).\n\n"+
			"closeTurn already notifies for a NeedsYou verdict, and the heartbeat pushed again directly.",
			second.Title, second.Body, first.Body)
	case <-time.After(400 * time.Millisecond):
	}

	// The surviving push must be the one that says what actually happened — the numbers are the
	// whole point of a budget stop.
	if first.Body == "" || first.Body == "The agent stopped making progress — tap to review" {
		t.Errorf("the surviving push lost the budget detail: %q", first.Body)
	}
}
