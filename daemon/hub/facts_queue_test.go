package hub

import (
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Publishing facts must not block the session's event pump on a wedged client.
//
// broadcastFacts called sendEvent, which is a direct conn.Send — it blocks until the frame is
// written. That ran from the pump (usage updates, mode changes), so one client on a stalled TCP
// connection stopped event processing for that whole session: every other watcher went silent too.
// It is the exact failure the per-subscriber queue and its drop-the-slow-client rule exist to
// prevent, and it was the one pump broadcast that bypassed them.
//
// A subscriber whose queue is full stands in for the wedged client: with a queue, it is dropped and
// everyone else keeps receiving; with a direct write, the caller would block instead.
func TestFactsDoNotBlockOnAWedgedSubscriber(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}
	m := newManagedSession(h, &subSess{ch: make(chan agent.Event, 1)}, sessionMeta{})

	// A subscriber that never drains: capacity 1, already full.
	stuck := &subscriber{conn: subscriberConnID, ch: make(chan []byte, 1), done: make(chan struct{})}
	stuck.ch <- []byte("occupied")
	m.mu.Lock()
	m.subs[subscriberConnID] = stuck
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		h.broadcastFacts(m)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publishing facts blocked on a subscriber that was not draining — the session's pump is stuck")
	}
	_ = protocol.TypeSessionFacts
}
