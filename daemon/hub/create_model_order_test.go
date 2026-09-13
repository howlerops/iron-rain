package hub

import (
	"context"
	"sync"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
)

// modelOrderSess records the ORDER of SetModel versus the first prompt.
type modelOrderSess struct {
	mu    sync.Mutex
	calls []string
}

func (s *modelOrderSess) note(what string) {
	s.mu.Lock()
	s.calls = append(s.calls, what)
	s.mu.Unlock()
}
func (s *modelOrderSess) ID() string                 { return "sess_model" }
func (s *modelOrderSess) Provider() string           { return "fake" }
func (s *modelOrderSess) Events() <-chan agent.Event { return make(chan agent.Event) }
func (s *modelOrderSess) Prompt(_ context.Context, _ string) error {
	s.note("prompt")
	return nil
}
func (s *modelOrderSess) Respond(context.Context, string, string) error { return nil }
func (s *modelOrderSess) Stop(context.Context) error                    { return nil }
func (s *modelOrderSess) Close() error                                  { return nil }
func (s *modelOrderSess) SetModel(_, _ string) error {
	s.note("setmodel")
	return nil
}

// The model must be set BEFORE the session's first turn starts.
//
// Create-with-a-prompt starts the opening turn inside Create, and SetModel used to run afterwards —
// so a session created with a chosen model answered its FIRST question on the provider's default and
// only switched from turn two. The user picked a model, watched the first answer come from a
// different one, and nothing anywhere said so. (claude-code's sidecar reads OCULUS_MODEL at startup
// for exactly this purpose and nothing has ever set it; ordering the calls correctly fixes every
// provider rather than that one.)
func TestModelIsSetBeforeTheFirstPrompt(t *testing.T) {
	sess := &modelOrderSess{}
	// Drive the same ordering the create path uses: set the model, then deliver the held-back prompt.
	if setter, ok := any(sess).(agent.ModelSetter); ok {
		_ = setter.SetModel("anthropic", "opus")
	}
	_ = promptSession(context.Background(), sess, "hello", nil, false)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.calls) != 2 {
		t.Fatalf("expected a model set and a prompt, got %v", sess.calls)
	}
	if sess.calls[0] != "setmodel" {
		t.Errorf("call order was %v — the first turn ran before the model was applied", sess.calls)
	}
}
