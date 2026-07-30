package hub

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

func TestMCPSessionTokenLifecycle(t *testing.T) {
	tk := newMCPSessionTokens()
	token := tk.mint()
	if token == "" {
		t.Fatal("mint must return a token")
	}
	// Unbound: not yet attributable to a session.
	if _, ok := tk.session(token); ok {
		t.Error("an unbound token must not resolve to a session")
	}
	tk.bind(token, "sess-1")
	if id, ok := tk.session(token); !ok || id != "sess-1" {
		t.Fatalf("bound token resolved to %q/%v", id, ok)
	}
	// Revoking the session invalidates the token — a leaked config must not outlive its session.
	if got := tk.revoke("sess-1"); got != token {
		t.Errorf("revoke returned %q, want the minted token", got)
	}
	if _, ok := tk.session(token); ok {
		t.Error("a revoked token must stop resolving")
	}
	// Two tokens are distinct.
	if tk.mint() == tk.mint() {
		t.Error("minted tokens must be unique")
	}
}

// TestMCPToolDeniedByReadOnlyMode is the hole this closes: without it, an agent in a read-only mode
// could still reach out through an MCP server and mutate the machine.
func TestMCPToolDeniedByReadOnlyMode(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	h.mu.Lock()
	h.sessions[fake.ID()] = m
	h.mu.Unlock()
	m.mu.Lock()
	m.mode = protocol.ModeAsk
	m.mu.Unlock()

	token := h.mcpTokens.mint()
	h.mcpTokens.bind(token, fake.ID())

	err := h.authorizeMCPTool(context.Background(), token, "github", "create_issue")
	if err == nil {
		t.Fatal("a mutating MCP tool must be denied in read-only mode")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("the denial should explain itself, got %q", err)
	}
}

// TestMCPToolAllowedByStandingRule: an existing "always allow" rule covers the qualified MCP name,
// so the user isn't re-asked for a tool they already blessed.
func TestMCPToolAllowedByStandingRule(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	h.mu.Lock()
	h.sessions[fake.ID()] = m
	h.mu.Unlock()

	h.addApprovalRule(ApprovalRule{Provider: "fake", Tool: "mcp:github:create_issue", Action: protocol.DecisionAllow})
	token := h.mcpTokens.mint()
	h.mcpTokens.bind(token, fake.ID())

	if err := h.authorizeMCPTool(context.Background(), token, "github", "create_issue"); err != nil {
		t.Fatalf("a standing allow rule should permit the call, got %v", err)
	}
	// A DIFFERENT tool on the same server is still gated (the rule is per-tool, not per-server).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := h.authorizeMCPTool(ctx, token, "github", "delete_repo"); err == nil {
		t.Error("an unrelated tool must not inherit another tool's rule")
	}
}

// TestMCPToolDeniedByStandingDeny: deny rules apply to MCP tools too.
func TestMCPToolDeniedByStandingDeny(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	h.mu.Lock()
	h.sessions[fake.ID()] = m
	h.mu.Unlock()

	h.addApprovalRule(ApprovalRule{Tool: "mcp:github:delete_repo", Action: "deny"})
	token := h.mcpTokens.mint()
	h.mcpTokens.bind(token, fake.ID())

	err := h.authorizeMCPTool(context.Background(), token, "github", "delete_repo")
	if err == nil || !strings.Contains(err.Error(), "denies") {
		t.Fatalf("a deny rule must block the call, got %v", err)
	}
}

// TestMCPUnknownTokenIsAllowed: the machine-wide token carries no session, so no policy can be
// attributed to it. That path must not be broken by the session gating.
func TestMCPUnknownTokenIsAllowed(t *testing.T) {
	h := New()
	if err := h.authorizeMCPTool(context.Background(), "some-machine-token", "github", "anything"); err != nil {
		t.Fatalf("an unattributed token should pass through, got %v", err)
	}
}

// TestMCPApprovalResolvesWaiter: answering the card unblocks the held call.
func TestMCPApprovalResolvesWaiter(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	h.mu.Lock()
	h.sessions[fake.ID()] = m
	h.mu.Unlock()
	token := h.mcpTokens.mint()
	h.mcpTokens.bind(token, fake.ID())

	done := make(chan error, 1)
	go func() { done <- h.authorizeMCPTool(context.Background(), token, "github", "read_issue") }()

	// Wait for the approval to be registered, then answer it.
	var id string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && id == "" {
		h.mu.Lock()
		for k := range h.mcpApprovals {
			id = k
		}
		h.mu.Unlock()
		if id == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if id == "" {
		t.Fatal("no MCP approval was raised")
	}
	if !h.resolveMCPApproval(id, protocol.DecisionAllow) {
		t.Fatal("resolveMCPApproval should recognize its own approval id")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an allowed call should proceed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the held call never unblocked")
	}
	// A native approval id must NOT be captured by the MCP path.
	if h.resolveMCPApproval("ap-native", protocol.DecisionAllow) {
		t.Error("resolveMCPApproval must ignore ids it didn't create")
	}
}
