package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// handleUnreachable must not abandon a turn it was not watching.
//
// It guarded its writes with "a turn is open", never with "is it MY turn". turnLoops does check
// stillMine — but AFTER handleUnreachable returns, which is too late for the two writes inside it.
// Revive blocks for up to 20s, and that is exactly the window in which a user hits Stop and sends
// the redirect Stop exists for: turn A closes, turn B opens, and the abandon then killed turn B.
//
// From the user's side the redirect they just sent dies instantly with "abandoned: agent unreachable
// for 2m30s", its tool cards seal as "the turn ended", and a needs-you push goes out — while the
// agent works on it.
func TestAnUnreachableProbeDoesNotAbandonTheNextTurn(t *testing.T) {
	m, _, _ := turnHarness(t, func(ctx context.Context) (bool, error) { return true, nil })

	m.openTurn("")
	stale := m.currentTurnID(t)
	m.closeTurn(protocol.StatusIdle, "finished normally")

	// The redirect the user typed after pressing Stop.
	m.openTurn("")
	live := m.currentTurnID(t)
	if live == stale {
		t.Fatal("the second openTurn did not start a new turn")
	}

	// Turn A's supervisor finally gives up, long past its window. Force the elapsed-time branch by
	// backdating the probe clock, which is what a real 2m+ outage does.
	m.mu.Lock()
	m.turnProbeSince = m.turnStartedAt.Add(-time.Hour)
	m.mu.Unlock()
	m.handleUnreachable(errors.New("dial tcp: connection refused"), 3, 3, stale)

	m.mu.Lock()
	phase, id := m.turnPhase, m.turnID
	m.mu.Unlock()
	if phase == "" || id != live {
		t.Fatalf("turn %q was abandoned by turn %q's supervisor (phase=%q id=%q).\n\n"+
			"The user's redirect dies instantly as 'agent unreachable', its tool cards seal, and a "+
			"needs-you push fires while the agent is working on it.", live, stale, phase, id)
	}
}

// And the ordinary case must still work: a supervisor abandoning its OWN turn.
func TestAnUnreachableProbeStillAbandonsItsOwnTurn(t *testing.T) {
	m, _, _ := turnHarness(t, func(ctx context.Context) (bool, error) { return true, nil })
	m.openTurn("")
	mine := m.currentTurnID(t)

	m.mu.Lock()
	m.turnProbeSince = m.turnStartedAt.Add(-time.Hour)
	m.mu.Unlock()
	m.handleUnreachable(errors.New("dial tcp: connection refused"), 3, 3, mine)

	m.mu.Lock()
	phase := m.turnPhase
	m.mu.Unlock()
	if phase != "" {
		t.Fatalf("a genuinely unreachable turn was left open (phase=%q) — the identity check must "+
			"narrow the abandon, not disable it", phase)
	}
}
