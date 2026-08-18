package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
)

// nudgeFakeSess is a turnFakeSess that can also be nudged, recording every nudge it receives. The
// split matters: a session that does NOT implement agent.Nudger must be escalated to a human
// instead of being nudged, and these tests assert both halves.
type nudgeFakeSess struct {
	turnFakeSess
	nudges  chan string
	nudgeMu atomic.Int32
}

func (f *nudgeFakeSess) Nudge(_ context.Context, text string) error {
	f.nudgeMu.Add(1)
	select {
	case f.nudges <- text:
	default:
	}
	return nil
}

// stallHarness builds a managedSession whose turn will go stalled almost immediately: the provider
// always reports busy (the wedged-tool signature — opencode reads an incomplete assistant message
// for a hung tool exactly as it does for one that's thinking), while nothing ever progresses.
func stallHarness(t *testing.T, nudgeable bool) (*managedSession, *nudgeFakeSess, chan []byte) {
	t.Helper()
	busy := func(context.Context) (bool, error) { return true, nil }
	fake := &nudgeFakeSess{
		turnFakeSess: turnFakeSess{ch: make(chan agent.Event, 8), probe: busy},
		nudges:       make(chan string, 8),
	}
	var sess agent.Session = fake
	if !nudgeable {
		sess = &plainFakeSess{turnFakeSess: turnFakeSess{ch: fake.ch, probe: busy}}
	}
	m := newManagedSession(New(), sess, sessionMeta{})
	m.hbEvery, m.quietAfter, m.reconcileTick, m.probeFailLimit = 30*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond, 3
	m.noProgressFor, m.nudgeLimit = 40*time.Millisecond, 2
	frames := make(chan []byte, 256)
	m.mu.Lock()
	m.subs[nil] = &subscriber{conn: nil, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()
	return m, fake, frames
}

// plainFakeSess is a session with NO Nudge method — the generic CLI adapter's shape, which refuses
// any prompt while its turn runs.
type plainFakeSess struct{ turnFakeSess }

// TestTurnStalledNudgesBeforeGivingUp is the core of the "keep it moving" contract: a turn the
// provider swears is busy but which has stopped progressing must be NUDGED, repeatedly, and only
// after the nudges are spent may it end — and it must end as needs_you, never as an error.
//
// Before this existed the reconciler had exactly two answers for a quiet turn: heartbeat "working"
// forever (the hang), or declare it abandoned and page the user about an agent error. Neither one
// ever tried to get the agent moving again.
func TestTurnStalledNudgesBeforeGivingUp(t *testing.T) {
	m, fake, frames := stallHarness(t, true)
	m.openTurn("running glob")

	// It goes stalled rather than staying "running" forever.
	nextTurnState(t, frames, "stalled", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusStalled
	})

	// And it actually nudges — twice, the configured limit.
	for i := 1; i <= 2; i++ {
		select {
		case text := <-fake.nudges:
			if text == "" {
				t.Fatalf("nudge %d was empty — the agent has nothing to act on", i)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("stalled turn never sent nudge %d — it just sat there", i)
		}
	}

	// Then, and only then, it hands over to a human — as needs_you, NOT error/abandoned.
	ts := nextTurnState(t, frames, "needs_you", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusNeedsYou || ts.State == protocol.StatusError || ts.State == "abandoned"
	})
	if ts.State != protocol.StatusNeedsYou {
		t.Fatalf("a stuck turn ended as %q — stuck is not an error, and filing it as one is how "+
			"people learn to ignore the notification", ts.State)
	}
	if ts.Reason == "" {
		t.Fatal("needs_you with no reason — the user has no idea what to look at")
	}
}

// TestTurnStalledRecoversWithoutBotheringAnyone: if the nudge works, the turn goes back to running
// and nobody is paged. This is the case that should be common, and it is the reason we nudge at all
// instead of going straight to a notification.
func TestTurnStalledRecoversWithoutBotheringAnyone(t *testing.T) {
	m, fake, frames := stallHarness(t, true)
	m.openTurn("running glob")

	select {
	case <-fake.nudges:
	case <-time.After(3 * time.Second):
		t.Fatal("never nudged")
	}

	// The agent picks the nudge up and gets back to work: a tool starts, which is real progress.
	m.turnOnTool(protocol.SessionTool{SessionID: m.sess.ID(), ID: "tl_2", Name: "bash", Status: "running"})
	m.turnOnStatus(protocol.SessionStatus{SessionID: m.sess.ID(), Status: protocol.StatusRunning})

	nextTurnState(t, frames, "back to running", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusRunning
	})
	m.closeTurn(protocol.StatusIdle, "")
}

// TestTurnUnnudgeableStallGoesStraightToNeedsYou: a provider that cannot take a message mid-turn
// (the generic CLI adapter rejects one outright) must be handed to a human immediately rather than
// nudged into the void — and STILL not be reported as an error.
func TestTurnUnnudgeableStallGoesStraightToNeedsYou(t *testing.T) {
	m, _, frames := stallHarness(t, false)
	m.openTurn("running glob")

	ts := nextTurnState(t, frames, "terminal", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusNeedsYou || ts.State == protocol.StatusError || ts.State == "abandoned"
	})
	if ts.State != protocol.StatusNeedsYou {
		t.Fatalf("un-nudgeable stall ended as %q, want needs_you", ts.State)
	}
}

// TestTurnSealsUnfinishedToolCards is the regression for the immortal "running" tool card: a tool
// whose completion event never arrives must be resolved when the turn ends.
//
// Sub-agent children have been sealed at turn close for a while; tool cards were not, so a lost
// completion left a glob spinning in the transcript forever — surviving even the turn that spawned
// it, with no way back short of restarting the app.
func TestTurnSealsUnfinishedToolCards(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.openTurn("")
	m.turnOnTool(protocol.SessionTool{
		SessionID: m.sess.ID(), ID: "tl_1", Name: "glob", Title: "**/*.go", Status: "running",
	})

	m.closeTurn(protocol.StatusIdle, "")

	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-frames:
			env, err := protocol.Decode(raw)
			if err != nil || env.Type != protocol.TypeSessionTool {
				continue
			}
			var st protocol.SessionTool
			if json.Unmarshal(env.Payload, &st) != nil {
				continue
			}
			if st.ID != "tl_1" {
				continue
			}
			if st.Status == "running" {
				continue // the original frame, not the seal
			}
			if st.Output == "" {
				t.Fatal("sealed the card but said nothing — it reads as though the tool failed")
			}
			if st.Name != "glob" {
				t.Fatalf("seal lost the card's identity: %+v", st)
			}
			return
		case <-deadline:
			t.Fatal("the turn ended and the running tool card was never sealed — it spins forever")
		}
	}
}

// TestTurnStalledMarksTheQuietChild: a fan-out's parent clock is bumped by ANY child, so one chatty
// sub-agent used to make nine dead ones invisible. The stalled verdict must name the quiet one.
func TestTurnStalledMarksTheQuietChild(t *testing.T) {
	m, fake, _ := stallHarness(t, true)
	m.openTurn("")
	m.turnOnChild(protocol.SubAgent{ParentID: m.sess.ID(), ID: "kid_quiet", Title: "explore", Status: "started"})

	// Backdate the child's own clock: it started, then went silent.
	m.mu.Lock()
	m.turnKids["kid_quiet"].LastEventAt = time.Now().Add(-time.Hour).Unix()
	m.mu.Unlock()

	select {
	case text := <-fake.nudges:
		if text == "" {
			t.Fatal("empty nudge")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("never nudged a turn with a stalled child")
	}

	m.mu.Lock()
	state := m.turnKids["kid_quiet"].State
	m.mu.Unlock()
	if state != protocol.StatusStalled {
		t.Fatalf("quiet child is still %q — a stalled sub-agent stays invisible behind its siblings", state)
	}
	m.closeTurn(protocol.StatusIdle, "")
}

// TestInterruptDoesNotPageTheUser is the regression for being notified about your own Stop press.
//
// session.interrupt aborts the turn, and the provider's answer to an abort is frequently an error
// (or nothing at all). Only session.stop used to set a flag suppressing the error verdict, so an
// interrupt reached publishVerdict looking exactly like a spontaneous agent death and fired both an
// "agent stopped responding" feed item and a push.
func TestInterruptDoesNotPageTheUser(t *testing.T) {
	h := New()
	h.SetActivity(activity.New(filepath.Join(t.TempDir(), "activity.jsonl"), 100))
	notes := make(chan push.Notification, 8)
	h.mu.Lock()
	h.notifier = recNotifier{ch: notes}
	h.pushTokens = []string{"device-1"}
	h.pushConcurrency, h.pushTimeout = 1, 2*time.Second
	h.mu.Unlock()

	fake := &turnFakeSess{ch: make(chan agent.Event, 8), probe: func(context.Context) (bool, error) { return true, nil }}
	m := newManagedSession(h, fake, sessionMeta{})
	m.hbEvery, m.quietAfter, m.reconcileTick, m.probeFailLimit = time.Hour, time.Hour, time.Hour, 3
	m.openTurn("")
	m.mu.Lock()
	m.wasRunning = true // a real turn was in flight, which is what gates the verdict's feed items
	m.mu.Unlock()

	// What the interrupt handler does.
	m.markUserInterrupted()
	m.closeTurn(protocol.StatusIdle, "interrupted by you")
	// And the provider answering the abort with an error afterwards must not resurrect the page.
	m.turnOnStatus(protocol.SessionStatus{SessionID: m.sess.ID(), Status: protocol.StatusError, Detail: "aborted"})

	select {
	case n := <-notes:
		t.Fatalf("interrupting your own agent sent you a push: %+v", n)
	case <-time.After(300 * time.Millisecond):
	}
	for _, e := range h.activity.Recent() {
		if e.Kind == activity.KindError {
			t.Fatalf("interrupt filed an error in the activity feed: %+v", e)
		}
	}
}

// TestDelegatedWorkIsNotAStall is the regression for a false stall reported with a screenshot: a
// sub-agent visibly running `pnpm test` and browser suites, while underneath it the parent read
// "stalled (no progress for 4m0s), nudged 1/3".
//
// turnToolAt is the progress clock, and it was stamped only by the parent's OWN tool boundaries and
// by child start/finish. A child merely producing output did not count. So the moment a parent
// delegated, its clock froze — and a parent that looks idle is exactly what delegation LOOKS like.
// Nudging there interrupts a working fan-out and trains people to ignore the warning.
func TestDelegatedWorkIsNotAStall(t *testing.T) {
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.openTurn("")
	m.turnOnChild(protocol.SubAgent{ParentID: m.sess.ID(), ID: "kid", Title: "typescript-pro", Status: "started"})

	// Pretend the parent has done nothing itself for a long time — the child owns the work now.
	m.mu.Lock()
	m.turnToolAt = time.Now().Add(-time.Hour)
	m.mu.Unlock()

	// The child streams output, as a test run does.
	m.turnOnChildEvent("kid")

	m.mu.Lock()
	stuckFor := time.Since(m.turnToolAt)
	m.mu.Unlock()
	if stuckFor > time.Minute {
		t.Fatalf("a child's output did not count as progress for the parent (clock still %s stale) — "+
			"the parent would be declared stalled and nudged while its sub-agent works", stuckFor.Round(time.Second))
	}
	m.closeTurn(protocol.StatusIdle, "")
}

// TestNewTurnDoesNotInheritAnOldOutage: outage state is per-outage and cannot outlive its turn.
// Left set, the first transient hiccup on a BRAND NEW turn computes its duration from the previous
// turn's timestamp, sails past every window and abandons instantly — reporting something absurd
// like "unreachable for 30m30s" about a turn thirty seconds old.
func TestNewTurnDoesNotInheritAnOldOutage(t *testing.T) {
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })

	// A previous turn ended while an outage was being timed.
	m.mu.Lock()
	m.turnProbeSince = time.Now().Add(-30 * time.Minute)
	m.turnRevives = 3
	m.mu.Unlock()

	m.openTurn("")

	m.mu.Lock()
	since, revives := m.turnProbeSince, m.turnRevives
	m.mu.Unlock()
	if !since.IsZero() {
		t.Fatal("a new turn inherited the previous turn's outage clock — its first probe hiccup " +
			"would abandon it instantly with an inflated duration")
	}
	if revives != 0 {
		t.Fatal("a new turn inherited a depleted repair budget")
	}
	m.closeTurn(protocol.StatusIdle, "")
}
