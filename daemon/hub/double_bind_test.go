package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// bindSess reports whether it was closed, so a replaced binding can be observed being torn down.
type bindSess struct {
	ch     chan agent.Event
	closed atomic.Bool
}

func (s *bindSess) ID() string                                    { return "dup_sess" }
func (s *bindSess) Provider() string                              { return "fake" }
func (s *bindSess) Events() <-chan agent.Event                    { return s.ch }
func (s *bindSess) Prompt(context.Context, string) error          { return nil }
func (s *bindSess) Respond(context.Context, string, string) error { return nil }
func (s *bindSess) Stop(context.Context) error                    { return nil }
func (s *bindSess) Close() error                                  { s.closed.Store(true); return nil }

// Binding the same session id twice must retire the first binding.
//
// addSession assigned into h.sessions and walked away, so the previous managedSession kept its pump
// running — still subscribed to the provider and still broadcasting to the same subscribers. Two
// pumps for one conversation delivers every frame twice: the reply rendered doubled on screen and a
// single turn logged "turn end (idle)" three times. Reproduced live whenever anything attached an id
// the daemon already owned.
func TestSecondBindingRetiresTheFirst(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}

	first := &bindSess{ch: make(chan agent.Event, 4)}
	m1 := h.addSession(first, sessionMeta{})
	if m1 == nil {
		t.Fatal("first bind produced no session")
	}

	second := &bindSess{ch: make(chan agent.Event, 4)}
	m2 := h.addSession(second, sessionMeta{})
	if m2 == m1 {
		t.Fatal("the second bind reused the first binding")
	}

	// The map must point at the new one...
	if got := h.managed("dup_sess"); got != m2 {
		t.Fatal("the hub did not adopt the new binding")
	}
	// ...and the old provider session must be closed, which is what actually stops its stream.
	deadline := time.Now().Add(2 * time.Second)
	for !first.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !first.closed.Load() {
		t.Fatal("the replaced binding was left open — its pump keeps delivering, so every frame arrives twice")
	}
	if second.closed.Load() {
		t.Fatal("the new binding was closed")
	}
	_ = protocol.StatusIdle
}
