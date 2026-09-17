package hub

import (
	"strings"
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

// A budget stop must reach the user even when no turn is open.
//
// This is the case the previous fix silently removed. Routing the notification exclusively through
// closeTurn looked equivalent — closeTurn publishes a NeedsYou verdict, which pushes — but
// closeTurnFrom returns immediately when turnPhase is empty. A turn that crossed the budget and
// then ENDED before the next 25s tick (ordinary for short loop turns) therefore disarmed autonomy,
// logged the stop, and told nobody: the person who set a spend ceiling on an unattended agent had
// no way to learn it had been hit.
func TestBudgetStopNotifiesWhenNoTurnIsOpen(t *testing.T) {
	got := make(chan push.Notification, 8)
	h := New()
	h.notifier = &prRecordingNotifier{got: got}
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	m := newManagedSession(h, &prFakeSess{ch: make(chan agent.Event)}, sessionMeta{})
	m.mu.Lock()
	m.autonomous = true
	m.budgetUSD = 1.00
	m.costUSD = 1.25
	m.lastActivity = time.Now() // recent activity → "working", not exhausted-on-arrival
	m.mu.Unlock()
	// Deliberately NO openTurn: the turn already finished.

	h.mu.Lock()
	h.sessions[m.sess.ID()] = m
	h.mu.Unlock()

	h.heartbeatTick()

	p, ok := nextPush(t, got)
	if !ok {
		t.Fatal("a session stopped at its budget with no turn open pushed nothing. The money " +
			"ceiling fired, autonomy was disarmed, and the user was never told — which is " +
			"indistinguishable from the agent simply going quiet.")
	}
	if p.Category != "AGENT_STALLED" {
		t.Errorf("category = %q, want AGENT_STALLED", p.Category)
	}
	if !strings.Contains(p.Body, "budget") {
		t.Errorf("push body %q does not mention the budget, so the reason is lost", p.Body)
	}

	// Still exactly one: the whole point of routing through closeTurn was to stop double-buzzing.
	select {
	case second := <-got:
		t.Fatalf("a second push was sent (%q) — one budget stop must produce one notification",
			second.Body)
	case <-time.After(150 * time.Millisecond):
	}
}

// A session already over budget when autonomy is switched on must still be stopped.
//
// The guard used to read m.hbState, which the same tick had just overwritten with the derived state
// — and deriveState returns hbExhausted for the same cost >= budget condition the branch is gated
// on. So the guard answered its own question: the first tick that saw the overspend concluded it had
// already handled it, skipped the stop, and left autonomy ON. Reachable simply by enabling autonomy
// on a session that has already spent past the ceiling, which touches neither cost nor activity.
func TestBudgetStopFiresWhenAutonomyIsEnabledAfterOverspending(t *testing.T) {
	got := make(chan push.Notification, 8)
	h := New()
	h.notifier = &prRecordingNotifier{got: got}
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	m := newManagedSession(h, &prFakeSess{ch: make(chan agent.Event)}, sessionMeta{})
	m.mu.Lock()
	m.budgetUSD = 1.00
	m.costUSD = 1.25
	m.autonomous = false                              // not yet enrolled
	m.lastActivity = time.Now().Add(-2 * time.Minute) // idle long enough to derive exhausted
	m.mu.Unlock()

	h.mu.Lock()
	h.sessions[m.sess.ID()] = m
	h.mu.Unlock()

	// A tick while NOT autonomous still stamps hbState — that is what poisoned the old guard.
	h.heartbeatTick()

	m.mu.Lock()
	m.autonomous = true // the user flips the toggle; cost and activity are untouched
	m.mu.Unlock()

	h.heartbeatTick()

	if _, ok := nextPush(t, got); !ok {
		t.Fatal("enabling autonomy on a session already past its budget never fired the ceiling. " +
			"The agent is now running unattended with no spend limit in effect, and the user was " +
			"told nothing — the toggle they just set reports itself as on.")
	}
	m.mu.Lock()
	auto := m.autonomous
	m.mu.Unlock()
	if auto {
		t.Error("autonomy is still enabled after the budget stop")
	}
}

// Raising the budget must re-arm the ceiling.
//
// The latch is per-turn and cleared by openTurn. Without that clearing a session stopped once could
// never be stopped again however much it went on to spend — the guard would still read "handled",
// which is the failure mode the latch was introduced to remove, just deferred by one stop.
func TestARaisedBudgetRearmsTheCeiling(t *testing.T) {
	got := make(chan push.Notification, 8)
	h := New()
	h.notifier = &prRecordingNotifier{got: got}
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	m := newManagedSession(h, &prFakeSess{ch: make(chan agent.Event)}, sessionMeta{})
	m.mu.Lock()
	m.autonomous = true
	m.budgetUSD = 1.00
	m.costUSD = 1.25
	m.lastActivity = time.Now()
	m.mu.Unlock()

	h.mu.Lock()
	h.sessions[m.sess.ID()] = m
	h.mu.Unlock()

	h.heartbeatTick()
	if _, ok := nextPush(t, got); !ok {
		t.Fatal("the first budget stop did not fire")
	}

	// The user raises the ceiling and starts new work.
	m.mu.Lock()
	m.budgetUSD = 2.00
	m.autonomous = true
	m.mu.Unlock()
	m.openTurn("more work")
	m.mu.Lock()
	m.costUSD = 2.50 // and blows through the new ceiling too
	m.lastActivity = time.Now()
	m.mu.Unlock()

	h.heartbeatTick()
	if _, ok := nextPush(t, got); !ok {
		t.Fatal("the raised budget was never enforced: the per-turn latch was not cleared, so this " +
			"session can never be stopped again no matter what it spends")
	}
}

// An interrupted session that then trips its budget must still be told.
//
// publishVerdict suppresses on userStopped/userInterrupted, which is right for an ordinary verdict:
// a user who stopped a turn does not need a notification about the turn they just stopped. It is
// wrong for a spend ceiling, which is the daemon's own conclusion — and the two coincide inside a
// single 25s heartbeat window, so this is reachable rather than theoretical.
func TestAnInterruptedSessionStillReportsItsBudgetStop(t *testing.T) {
	got := make(chan push.Notification, 8)
	h := New()
	h.notifier = &prRecordingNotifier{got: got}
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	m := newManagedSession(h, &prFakeSess{ch: make(chan agent.Event)}, sessionMeta{})
	m.mu.Lock()
	m.autonomous = true
	m.budgetUSD = 1.00
	m.costUSD = 1.25
	m.lastActivity = time.Now()
	m.userInterrupted = true // the user hit Stop moments before the tick
	m.mu.Unlock()

	h.mu.Lock()
	h.sessions[m.sess.ID()] = m
	h.mu.Unlock()

	h.heartbeatTick()

	if _, ok := nextPush(t, got); !ok {
		t.Fatal("a session that was interrupted and then crossed its spend ceiling reported " +
			"nothing. The user-stop suppression is for the turn the user stopped, not for a money " +
			"limit the daemon enforced afterwards.")
	}
}
