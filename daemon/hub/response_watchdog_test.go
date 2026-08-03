package hub

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

type fastApprovalSession struct {
	ch chan agent.Event
}

func (s *fastApprovalSession) ID() string                 { return "fast-ap" }
func (s *fastApprovalSession) Provider() string           { return "opencode" }
func (s *fastApprovalSession) Events() <-chan agent.Event { return s.ch }
func (s *fastApprovalSession) Prompt(context.Context, string) error {
	s.ch <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{
		ApprovalID: "perm-fast", SessionID: s.ID(), Tool: "bash", Detail: "npm test",
	}}
	return nil
}
func (s *fastApprovalSession) Respond(context.Context, string, string) error { return nil }
func (s *fastApprovalSession) Stop(context.Context) error                    { return nil }
func (s *fastApprovalSession) Close() error                                  { return nil }

func TestResponseWatchdogClearsOnFastApproval(t *testing.T) {
	h := New()
	fake := &fastApprovalSession{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	go m.run()
	defer close(fake.ch)

	m.armResponseWatchdog()
	if err := fake.Prompt(context.Background(), "run tests"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		awaiting := m.awaitingResponse
		m.mu.Unlock()
		if !awaiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("response watchdog stayed armed after a fast approval event")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
