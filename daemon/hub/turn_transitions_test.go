package hub

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Turn Engine stage 5, part A: the ILLEGAL (state, input) pairs.
//
// The tests around this file cover the legal path — open, progress, close — thoroughly. What none of
// them covers is the input that arrives when no turn is open, or after one has closed, which is not
// an exotic case at all: a provider's completion event routinely lands after the reconciler has
// already closed the turn on a probe, a sub-agent's finish arrives after its parent ended, and every
// close path can be reached twice by two goroutines that both believe they got there first.
//
// The invariant is the same for all of them: a late or duplicate input must be DROPPED, not applied.
// Applying one re-opens a turn nobody is working — the session shows "working…" with no agent behind
// it, and nothing will ever close it, because the thing that would have closed it already did.
//
// ON THE NEGATIVE CONTROL, because it is not the usual one-line revert. The child and tool cases are
// guarded TWICE — once where the input is folded in (turnOnChild's `turnPhase == ""` check) and
// again at the publish boundary (emitTurn's identical check) — so reverting either one alone leaves
// this table green. That is defence in depth working, not a tautological test: reverting BOTH fails
// it, with `published turn.state="" after the turn closed idle`. Anyone tightening this file should
// verify against that control rather than the single-guard one, which proves nothing either way.

// closedTurnCases is the table. Each case performs one input against a session whose turn is already
// closed, and the assertion is uniform: the turn does not come back.
var closedTurnCases = []struct {
	name string
	// why records the real event this models, so a future reader can tell whether a change in
	// provider behaviour has made the case obsolete or made it more important.
	why string
	do  func(m *managedSession)
}{
	{
		name: "idle after close",
		why:  "the provider's completion event arrives after the reconciler already closed the turn",
		do: func(m *managedSession) {
			m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusIdle})
		},
	},
	{
		name: "error after close",
		why:  "opencode reports a failed turn on TWO channels; the second one lands after the first closed it",
		do: func(m *managedSession) {
			m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusError,
				Detail: "the second error channel"})
		},
	},
	{
		name: "child started after parent close",
		why:  "a sub-agent's start event overtakes the parent's own completion",
		do: func(m *managedSession) {
			m.turnOnChild(protocol.SubAgent{ParentID: "t1", ID: "kid", Status: "started"})
		},
	},
	{
		name: "child finished after parent close",
		why:  "the ordinary case: children finish after the parent turn is sealed",
		do: func(m *managedSession) {
			m.turnOnChild(protocol.SubAgent{ParentID: "t1", ID: "kid", Status: "done"})
		},
	},
	{
		name: "child event after parent close",
		why:  "a child still streaming output into a turn that is over",
		do:   func(m *managedSession) { m.turnOnChildEvent("kid") },
	},
	{
		name: "tool after close",
		why:  "a tool result arriving after the turn it belonged to ended",
		do: func(m *managedSession) {
			m.turnOnTool(protocol.SessionTool{SessionID: "t1", ID: "tool1", Name: "bash",
				Status: "running"})
		},
	},
	{
		name: "bare event after close",
		why:  "any provider event at all on a closed turn",
		do:   func(m *managedSession) { m.noteTurnEvent() },
	},
	{
		name: "close again",
		why:  "two goroutines racing to the same verdict, or a stream end after a reconciled close",
		do:   func(m *managedSession) { m.closeTurn(protocol.StatusIdle, "") },
	},
	{
		name: "close again with a different verdict",
		why:  "the stream ends (abandoned) moments after the reconciler closed the turn (idle)",
		do:   func(m *managedSession) { m.closeTurn(protocol.StatusAbandoned, "stream ended") },
	},
}

// TestLateInputsDoNotResurrectAClosedTurn runs the table.
func TestLateInputsDoNotResurrectAClosedTurn(t *testing.T) {
	for _, tc := range closedTurnCases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return false, nil })
			m.openTurn("working")
			nextTurnState(t, frames, "running", func(ts protocol.TurnState) bool {
				return ts.State == protocol.StatusRunning
			})
			m.closeTurn(protocol.StatusIdle, "")
			nextTurnState(t, frames, "idle", func(ts protocol.TurnState) bool {
				return ts.State == protocol.StatusIdle
			})

			m.mu.Lock()
			lastEventBefore := m.turnLastEvent
			toolAtBefore := m.turnToolAt
			m.mu.Unlock()
			time.Sleep(2 * time.Millisecond) // so a clock that DOES move is measurably different

			tc.do(m)

			// The phase is the state machine's own record of whether a turn is open. Read directly
			// rather than through the wire here: the point of this table is the MACHINE, and a
			// resurrection that emitted no frame would still be a resurrection.
			//
			// The phase alone is NOT enough for every row. noteTurnEvent, turnOnChildEvent and
			// turnOnTool cannot assign turnPhase at all, so for those rows the phase check asserts
			// something the functions structurally cannot violate — it passes with their guards
			// removed. What those three CAN corrupt is the state a later turn inherits, so they are
			// checked on that instead.
			m.mu.Lock()
			phase := m.turnPhase
			toolsAfter := len(m.turnTools)
			kidsAfter := len(m.turnKids)
			// The liveness clocks are what noteTurnEvent and turnOnChildEvent actually write, and
			// therefore the only thing those two rows can corrupt. Checking turnPhase for them
			// asserted against functions that cannot assign it — the row passed with the guard
			// removed, which is how two of these survived a round of "fixing" the table.
			lastEventAfter := m.turnLastEvent
			toolAtAfter := m.turnToolAt
			m.mu.Unlock()
			if phase != "" {
				t.Fatalf("%s re-opened the turn (phase %q).\n\n%s\n\nNothing is working on this "+
					"session, so nothing will ever close it again: the client spins until the app "+
					"is restarted.", tc.name, phase, tc.why)
			}

			// A closed turn's liveness clocks must not advance. They feed the stall detector and the
			// reconciler, so a late event nudging them forward makes the NEXT turn look like it has
			// just progressed when nothing has.
			if !lastEventAfter.Equal(lastEventBefore) {
				t.Errorf("%s moved turnLastEvent on a closed turn (%v → %v).\n\n%s\n\nThe next "+
					"turn inherits a progress clock that already looks fresh.",
					tc.name, lastEventBefore, lastEventAfter, tc.why)
			}
			if !toolAtAfter.Equal(toolAtBefore) {
				t.Errorf("%s moved turnToolAt on a closed turn (%v → %v).\n\n%s",
					tc.name, toolAtBefore, toolAtAfter, tc.why)
			}

			// A closed turn holds no live tool or child state. closeTurn nils turnTools on the way
			// out, so anything repopulating it here is state a subsequent turn would inherit: a
			// phantom outstanding tool or a child card that spins with nothing behind it.
			if toolsAfter != 0 {
				t.Fatalf("%s left %d tool(s) on a closed turn.\n\n%s\n\nThe next turn inherits "+
					"them as outstanding work that will never complete.", tc.name, toolsAfter, tc.why)
			}
			if kidsAfter != 0 {
				t.Fatalf("%s left %d sub-agent(s) on a closed turn.\n\n%s\n\nThey render as "+
					"running cards with no agent behind them.", tc.name, kidsAfter, tc.why)
			}

			// And no further turn.state may be published — a client that already rendered the turn
			// as finished would show it running again.
			deadline := time.After(120 * time.Millisecond)
			for {
				select {
				case raw := <-frames:
					ts, ok := decodeTurn(raw)
					if !ok {
						continue
					}
					if ts.State != protocol.StatusIdle {
						t.Fatalf("%s published turn.state=%q after the turn closed idle", tc.name, ts.State)
					}
				case <-deadline:
					return
				}
			}
		})
	}
}

// The mirror image: the SAME inputs with no turn ever opened must also do nothing.
//
// This is not the same test. Above, the machine can tell a late input from a live one because it has
// a closed turn to compare against. Here it has nothing at all — the session was restored, or the
// provider started streaming before anyone prompted it — and "no turn has ever existed" is exactly
// the state in which an unguarded map write panics rather than misbehaving quietly.
func TestInputsWithNoOpenTurnAreDropped(t *testing.T) {
	for _, tc := range closedTurnCases {
		if tc.name == "close again" || tc.name == "close again with a different verdict" {
			continue // closing a turn that was never opened is covered by TestTurnLifecycle
		}
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return false, nil })
			tc.do(m) // no openTurn: nothing has ever run here
			m.mu.Lock()
			phase := m.turnPhase
			m.mu.Unlock()
			if phase != "" {
				t.Fatalf("%s opened a turn on a session that has never run one (phase %q) — the "+
					"session shows as working with no agent behind it", tc.name, phase)
			}
		})
	}
}

// The one input that SHOULD re-open a turn, and the property that makes it safe.
//
// StatusRunning after a close is not a late event — it is how a provider starts a turn nobody
// prompted (an auto-continue, a resumed session), and refusing it would leave real work invisible.
// It is in neither table above for that reason. What it must NOT do is resurrect the OLD turn: a new
// turn needs a new id, or the client files the new output under a turn it has already rendered as
// finished, and the reconciler's stillMine guard — which is what stops a stale goroutine closing a
// live turn — compares against an id that is no longer unique to one turn.
func TestProviderRunningAfterACloseOpensAFreshTurn(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.openTurn("first")
	first := nextTurnState(t, frames, "running", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusRunning
	})
	m.closeTurn(protocol.StatusIdle, "")
	nextTurnState(t, frames, "idle", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusIdle
	})

	m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusRunning,
		Detail: "second"})

	second := nextTurnState(t, frames, "the second turn", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusRunning
	})
	if second.TurnID == "" {
		t.Fatal("the re-opened turn has no id")
	}
	if second.TurnID == first.TurnID {
		t.Fatalf("the provider's new turn reused the closed turn's id (%s). Output from the new "+
			"turn is filed under one the client already finished, and stillMine can no longer tell "+
			"the two apart.", second.TurnID)
	}
}
