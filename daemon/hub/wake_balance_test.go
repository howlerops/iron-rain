package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// countingGuard stands in for wake.Guard. Hold can be made to block, which is how the ordering below
// is tested without relying on winning a race.
type countingGuard struct {
	mu       sync.Mutex
	n        int
	holdGate chan struct{} // if non-nil, Hold blocks on it
}

func (g *countingGuard) Hold() {
	if gate := g.gate(); gate != nil {
		<-gate
	}
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
}

func (g *countingGuard) Release() {
	g.mu.Lock()
	if g.n > 0 {
		g.n-- // wake.Guard floors at zero; mirroring that is the whole point of this test
	}
	g.mu.Unlock()
}

func (g *countingGuard) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

func (g *countingGuard) gate() chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holdGate
}

// A turn must not be observable before its sleep assertion has been taken.
//
// openTurn published turnPhase under m.mu and called Hold AFTER releasing it. From that unlock the
// turn is closeable by anyone — the heartbeat's budget path, or a stale supervisor. Guard.Release
// deliberately ignores a release at zero so the count cannot go negative, which means a Release that
// arrives BEFORE its matching Hold is silently discarded and the refcount is then permanently one too
// high. `caffeinate -s` runs for the life of the daemon and the Mac stops idle-sleeping, with nothing
// on any surface reporting it.
//
// The invariant that closes it: Hold strictly precedes the turn becoming visible. Asserted here by
// blocking inside Hold and checking that no turn appears while it is blocked.
func TestATurnIsNeverVisibleBeforeItsSleepAssertion(t *testing.T) {
	gate := make(chan struct{})
	g := &countingGuard{holdGate: gate}
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.hub.awake = g

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.openTurn("")
	}()

	// While Hold is blocked, nothing may see an open turn.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		phase := m.turnPhase
		m.mu.Unlock()
		if phase != "" {
			close(gate)
			<-done
			t.Fatal("the turn became visible before its sleep assertion was taken. A closer reaching " +
				"it in this window releases at zero, that release is discarded, and the machine is " +
				"held awake for the life of the daemon.")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	<-done

	if got := g.count(); got != 1 {
		t.Fatalf("holds = %d, want 1 after opening one turn", got)
	}
	m.closeTurn(protocol.StatusIdle, "done")
	if got := g.count(); got != 0 {
		t.Errorf("holds = %d after the turn closed, want 0 — the assertion outlived its turn", got)
	}
}

// Refreshing an already-open turn must not accumulate holds. openTurn is idempotent and is called on
// every provider StatusRunning, so a surplus Hold here would leak once per status event rather than
// once per turn — far faster than the race above.
func TestRefreshingAnOpenTurnDoesNotAccumulateHolds(t *testing.T) {
	g := &countingGuard{}
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.hub.awake = g

	m.openTurn("first")
	for i := 0; i < 5; i++ {
		m.openTurn("refresh") // idempotent: same turn, just a new detail
	}
	if got := g.count(); got != 1 {
		t.Fatalf("holds = %d after one turn and five refreshes, want 1", got)
	}
	m.closeTurn(protocol.StatusIdle, "done")
	if got := g.count(); got != 0 {
		t.Errorf("holds = %d after the turn closed, want 0 — every refresh leaked one, so a busy "+
			"session pins the machine awake within seconds", got)
	}
}
