package hub

import (
	"sync"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
)

// `meta.label` is MUTABLE: session.rename writes it under m.mu, on the connection's read loop.
// activityTitle read it with no lock at all, from the event pump, the turn-engine goroutine and the
// PR-check poller. An unsynchronized string read against a concurrent write can return a mismatched
// pointer/length pair, so a rename landing as a turn ends put a corrupted title into the activity
// feed and the needs-you inbox.
//
// Run under -race, which is how this package's suite runs.
func TestRenamingASessionWhileItsTitleIsReadIsNotARace(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}
	m := newManagedSession(h, &subSess{ch: make(chan agent.Event, 1)}, sessionMeta{label: "initial"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the rename path
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			m.mu.Lock()
			if i%2 == 0 {
				m.meta.label = "a considerably longer session name than the other one"
			} else {
				m.meta.label = "short"
			}
			m.mu.Unlock()
		}
	}()
	go func() { // the activity/turn/PR-check readers
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = m.activityTitle()
		}
	}()
	wg.Wait()
}
