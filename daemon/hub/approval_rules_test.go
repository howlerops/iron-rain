package hub

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

type approvalFakeSess struct {
	ch        chan agent.Event
	responded atomic.Value // "id|decision"
}

func (f *approvalFakeSess) ID() string                            { return "ap1sess" }
func (f *approvalFakeSess) Provider() string                      { return "fake" }
func (f *approvalFakeSess) Events() <-chan agent.Event            { return f.ch }
func (f *approvalFakeSess) Prompt(context.Context, string) error  { return nil }
func (f *approvalFakeSess) Stop(context.Context) error            { return nil }
func (f *approvalFakeSess) Close() error                          { return nil }
func (f *approvalFakeSess) Respond(_ context.Context, id, decision string) error {
	f.responded.Store(id + "|" + decision)
	return nil
}

// TestApprovalAlwaysPersistsAcrossSessions: a persisted ALWAYS rule auto-allows a matching approval
// silently (Respond called; request never broadcast), while an unmatched tool still surfaces.
func TestApprovalAlwaysPersistsAcrossSessions(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	h.rememberApprovalRule("fake", "bash") // the user's prior "Always" in an earlier session

	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	frames := make(chan []byte, 64)
	m.mu.Lock()
	m.subs[nil] = &subscriber{conn: nil, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()
	go m.run()

	// Matching approval → auto-allowed, never surfaced.
	fake.ch <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: "ap-1", SessionID: "ap1sess", Tool: "bash", Detail: "npm test"}}
	deadline := time.Now().Add(2 * time.Second)
	for fake.responded.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("matching approval was not auto-answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := fake.responded.Load().(string); got != "ap-1|"+protocol.DecisionAllow {
		t.Fatalf("responded %q, want ap-1|allow", got)
	}
	// The request must NOT have been broadcast to clients.
	drainDeadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case raw := <-frames:
			if env, err := protocol.Decode(raw); err == nil && env.Type == protocol.TypeApprovalRequest {
				t.Fatal("auto-allowed approval was still broadcast to clients")
			}
		case <-drainDeadline:
			break drain
		}
	}

	// A DIFFERENT tool still surfaces normally.
	fake.ch <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: "ap-2", SessionID: "ap1sess", Tool: "edit"}}
	surfaced := time.After(2 * time.Second)
	for {
		select {
		case raw := <-frames:
			if env, err := protocol.Decode(raw); err == nil && env.Type == protocol.TypeApprovalRequest {
				close(fake.ch)
				return // surfaced as expected
			}
		case <-surfaced:
			t.Fatal("unmatched approval was never surfaced")
		}
	}
}

// TestApprovalRulesPersistToDisk: rules survive a "restart" (fresh load from the same path).
func TestApprovalRulesPersistToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	h1 := New()
	h1.SetApprovalRulesPath(path)
	h1.rememberApprovalRule("opencode", "bash")

	h2 := New()
	h2.SetApprovalRulesPath(path) // the restarted daemon
	if d, ok := h2.evaluateApproval("opencode", "", protocol.ApprovalRequest{Tool: "bash", Detail: "npm test"}); !ok || d != protocol.DecisionAllow {
		t.Fatalf("ALWAYS rule did not survive restart (decision=%q matched=%v)", d, ok)
	}
}
