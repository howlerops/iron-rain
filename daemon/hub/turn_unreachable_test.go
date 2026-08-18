package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// reviveFakeSess is a turnFakeSess whose connection can be repaired: probes fail until Revive is
// called, after which they succeed. This is the shape of nearly every real outage — a transport that
// works again once something rebuilds it.
type reviveFakeSess struct {
	turnFakeSess
	revived  atomic.Int32
	reviveOK atomic.Bool
}

func (f *reviveFakeSess) Revive(context.Context) error {
	f.revived.Add(1)
	f.reviveOK.Store(true)
	return nil
}

// TestSlowProbeDoesNotAbandonAWorkingAgent is the regression for the reported incident:
//
//	agent unreachable: Get ".../session/ses_.../message?directory=...": context deadline exceeded
//
// The server was answering; the probe just took longer than its deadline, because that endpoint
// returns the session's ENTIRE history and the session was long. The old rule — four failed probes,
// which at a 5s tick is twenty seconds — then killed a turn whose agent was working perfectly. A
// timeout must buy patience, not a death sentence.
func TestSlowProbeDoesNotAbandonAWorkingAgent(t *testing.T) {
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return false, context.DeadlineExceeded })
	// A REFUSAL would be conclusive quickly; a timeout must not be, even long past that window.
	m.unreachWindow, m.slowWindow = 20*time.Millisecond, 10*time.Second
	m.openTurn("")

	time.Sleep(400 * time.Millisecond) // ≈ 40 reconcile ticks, all of them timing out

	m.mu.Lock()
	open := m.turnPhase != ""
	m.mu.Unlock()
	if !open {
		t.Fatal("a SLOW probe abandoned the turn — this is the 'context deadline exceeded' incident: " +
			"a live agent declared unreachable because its own history read outran the probe deadline")
	}
	m.closeTurn(protocol.StatusIdle, "")
}

// TestUnreachableAgentIsRepairedWithoutBotheringAnyone: when the connection can be rebuilt, the
// daemon rebuilds it and the turn carries on. Nobody is paged, nothing is abandoned, and the user
// never learns it happened — which is the entire point.
func TestUnreachableAgentIsRepairedWithoutBotheringAnyone(t *testing.T) {
	fake := &reviveFakeSess{}
	fake.ch = make(chan agent.Event, 8)
	fake.probe = func(context.Context) (bool, error) {
		if fake.reviveOK.Load() {
			return true, nil // the connection was repaired — the agent is there, and busy
		}
		return false, errRefused
	}
	m := newManagedSession(New(), fake, sessionMeta{})
	m.hbEvery, m.quietAfter, m.reconcileTick, m.probeFailLimit = 30*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond, 3
	m.unreachWindow, m.slowWindow, m.reviveLimit = 5*time.Second, 5*time.Second, 3
	frames := make(chan []byte, 256)
	m.mu.Lock()
	m.subs[nil] = &subscriber{conn: nil, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()

	m.openTurn("")

	// It reports that it is repairing rather than silently stalling...
	nextTurnState(t, frames, "recovering", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusRecovering
	})
	// ...and then gets back to running on its own.
	nextTurnState(t, frames, "back to running", func(ts protocol.TurnState) bool {
		return ts.State == protocol.StatusRunning
	})

	if fake.revived.Load() == 0 {
		t.Fatal("never attempted to repair the connection — an outage that could have healed itself " +
			"would have been reported to the user instead")
	}
	m.mu.Lock()
	open, phase := m.turnPhase != "", m.turnPhase
	m.mu.Unlock()
	if !open {
		t.Fatalf("turn closed despite a successful revive (phase %q)", phase)
	}
	m.closeTurn(protocol.StatusIdle, "")
}

// TestUnrecoverableAgentStillGetsReported: the other half of the contract. Self-healing must not
// become never-reporting — an agent that is genuinely gone, and stays gone through every repair
// attempt, has to surface. Silence would be worse than the old over-eager error.
func TestUnrecoverableAgentStillGetsReported(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return false, errRefused })
	m.unreachWindow, m.slowWindow = 60*time.Millisecond, 60*time.Millisecond
	m.openTurn("")

	ts := nextTurnState(t, frames, "abandoned", func(t2 protocol.TurnState) bool { return t2.State == "abandoned" })
	if ts.Reason == "" {
		t.Fatal("abandoned with no reason — the user has nothing to act on")
	}
	// The reason must say how long it was down, not just echo transport noise.
	if !contains(ts.Reason, "unreachable for") {
		t.Fatalf("reason should state the outage duration, got %q", ts.Reason)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
