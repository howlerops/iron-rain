package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A turn's supervisor must never act on a turn that is not its own.
//
// turnLoops consults `stop` only at the top of each iteration, and every blocking call in the body
// straddles that check: Probe (10s), Revive (20s), Nudge (15s), Recover (15s). So the turn could end
// and a NEW one open while one was in flight, and the loop would resume and act on m.turnPhase —
// whichever turn is open NOW. The worst path is the one below: a probe that answers "not busy" about
// the FINISHED turn makes the stale supervisor call Recover (re-broadcasting the old turn's output
// into the live stream) and then close the NEW turn as "reconciled: completion event was lost".
//
// From the user's side the prompt they just sent ends instantly: the composer unlocks, every
// in-flight tool card is sealed as "the turn ended before this tool reported a result", and the
// agent — still working — now has no heartbeat and no stall detection behind it.
func TestAStaleSupervisorDoesNotCloseTheNextTurn(t *testing.T) {
	// The probe parks until released, which is how a turn boundary is made to land INSIDE it.
	release := make(chan struct{})
	probed := make(chan struct{}, 1)
	var calls atomic.Int32
	m, fake, _ := turnHarness(t, func(ctx context.Context) (bool, error) {
		if calls.Add(1) == 1 {
			// The FIRST probe belongs to turn A. It parks, so the turn boundary lands inside it, and
			// then answers "not busy" — which was true of turn A and is the answer that drives the
			// reconcile-and-close path.
			probed <- struct{}{}
			<-release
			return false, nil
		}
		// Every later probe belongs to turn B, which is genuinely working. Its own supervisor must
		// therefore leave it alone; anything that closes it came from turn A.
		return true, nil
	})

	m.openTurn("")
	first := m.currentTurnID(t)

	// Wait until the supervisor is inside the probe.
	select {
	case <-probed:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never probed; the rest of this test would prove nothing")
	}

	// Turn A ends and turn B opens, entirely within the probe's lifetime.
	m.closeTurn(protocol.StatusIdle, "finished normally")
	m.openTurn("")
	second := m.currentTurnID(t)
	if second == first {
		t.Fatal("the second openTurn did not start a new turn")
	}

	close(release) // the stale probe now answers, about turn A

	// Turn B must be left completely alone. Two things would be wrong here, and Recover is the one
	// that happens FIRST and unconditionally: turn A's supervisor re-broadcasts turn A's final output
	// into turn B's live stream, and only then closes turn B as reconciled. Asserting on the close
	// alone is not enough — it is posted to the session pump, so whether it lands depends on the
	// pump, while the Recover has already happened by then.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := fake.recovered.Load(); n > 0 {
			t.Fatalf("a supervisor from turn %q called Recover %d time(s) while turn %q was open and "+
				"BUSY. That replays the previous turn's output into the live stream, and the close "+
				"that follows ends the prompt the user just sent.", first, n, second)
		}
		m.mu.Lock()
		phase, id := m.turnPhase, m.turnID
		m.mu.Unlock()
		if id != second {
			t.Fatalf("the turn identity changed to %q — a supervisor from a previous turn is driving "+
				"this session", id)
		}
		if phase == "" {
			t.Fatal("turn A's supervisor CLOSED turn B. The user's freshly-sent prompt ends instantly, " +
				"its tool cards are sealed as abandoned, and the agent keeps working with nothing " +
				"watching it.")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The other half: a supervisor bound to the OPEN turn must still do its job. Without this, "never
// act on another turn" is satisfiable by never acting at all.
func TestTheSupervisorStillReconcilesItsOwnTurn(t *testing.T) {
	m, fake, _ := turnHarness(t, func(ctx context.Context) (bool, error) {
		return false, nil // the provider says this turn is done; we lost the completion event
	})

	m.openTurn("")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		phase := m.turnPhase
		m.mu.Unlock()
		if phase == "" {
			if fake.recovered.Load() == 0 {
				t.Error("the turn was closed without recovering the output the provider still had")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a turn whose completion event was lost was never reconciled — the session would sit " +
		"\"working…\" forever, which is the entire reason this supervisor exists")
}

func (m *managedSession) currentTurnID(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turnPhase == "" {
		t.Fatal("no turn is open")
	}
	return m.turnID
}
