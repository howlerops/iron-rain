package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

func TestIsMutatingTool(t *testing.T) {
	mutating := []string{"edit", "Edit", "Write", "bash", "Bash", "patch", "NotebookEdit", "MultiEdit", "[sub-agent] bash"}
	for _, tool := range mutating {
		if !isMutatingTool(tool) {
			t.Errorf("%q should be treated as mutating", tool)
		}
	}
	readOnly := []string{"read", "Read", "Grep", "glob", "list", "WebSearch", "todowrite", "ExitPlanMode", "task"}
	for _, tool := range readOnly {
		if isMutatingTool(tool) {
			t.Errorf("%q should NOT be treated as mutating", tool)
		}
	}
	// Fail CLOSED: a tool we've never seen must be assumed dangerous in a read-only mode.
	if !isMutatingTool("SomeBrandNewHarnessTool") {
		t.Error("an unknown tool must default to mutating so read-only modes fail closed")
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		mode string
		plan bool
		want string
	}{
		{"", false, protocol.ModeCode},
		{"", true, protocol.ModeArchitect}, // the legacy Plan checkbox
		{"ask", false, protocol.ModeAsk},
		{"ASK", false, protocol.ModeAsk},
		{"plan", false, protocol.ModeArchitect}, // legacy alias
		{"architect", false, protocol.ModeArchitect},
		{"code", true, protocol.ModeCode}, // an explicit mode beats the legacy bool
		{"nonsense", false, protocol.ModeCode},
	}
	for _, c := range cases {
		if got := normalizeMode(c.mode, c.plan); got != c.want {
			t.Errorf("normalizeMode(%q, %v) = %q, want %q", c.mode, c.plan, got, c.want)
		}
	}
}

func TestModeDeniesTool(t *testing.T) {
	if !modeDeniesTool(protocol.ModeAsk, "bash") {
		t.Error("ask mode must deny bash")
	}
	if modeDeniesTool(protocol.ModeAsk, "read") {
		t.Error("ask mode must still allow reads")
	}
	if modeDeniesTool(protocol.ModeCode, "bash") {
		t.Error("code mode must not deny anything by itself")
	}
}

// TestAskModeDeniesEvenWithStandingAllow is the security property: a persisted "always allow bash"
// must NOT punch a hole through a read-only mode. Mode is checked before the rule engine.
func TestAskModeDeniesEvenWithStandingAllow(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	h.rememberApprovalRule("fake", "bash") // a broad standing allow

	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	m.mu.Lock()
	m.mode = protocol.ModeAsk
	m.subs[nil] = &subscriber{conn: nil, ch: make(chan []byte, 64), done: make(chan struct{})}
	m.mu.Unlock()
	go m.run()

	fake.ch <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{
		ApprovalID: "ap-ask", SessionID: "ap1sess", Tool: "bash", Detail: "rm -rf /"}}

	deadline := time.Now().Add(2 * time.Second)
	for fake.responded.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("ask mode never answered the approval")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := fake.responded.Load().(string); got != "ap-ask|"+protocol.DecisionDeny {
		t.Fatalf("responded %q — ask mode must DENY a mutating tool even with a standing allow rule", got)
	}
	close(fake.ch)
}

// TestAskModeStillAllowsReads: read-only mode must not block reading, or it's useless.
func TestAskModeStillAllowsReads(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	frames := make(chan []byte, 64)
	m.mu.Lock()
	m.mode = protocol.ModeAsk
	m.subs[nil] = &subscriber{conn: nil, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()
	go m.run()

	fake.ch <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{
		ApprovalID: "ap-read", SessionID: "ap1sess", Tool: "read", Detail: "/etc/hosts"}}

	surfaced := time.After(2 * time.Second)
	for {
		select {
		case raw := <-frames:
			if env, err := protocol.Decode(raw); err == nil && env.Type == protocol.TypeApprovalRequest {
				if fake.responded.Load() != nil {
					t.Fatal("a read must not be auto-denied in ask mode")
				}
				close(fake.ch)
				return
			}
		case <-surfaced:
			t.Fatal("the read approval was never surfaced to the user")
		}
	}
}
