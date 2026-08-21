package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"git *", "git commit -m hi", true},
		{"git *", "gitk", false},
		{"git push *", "git push origin main", true},
		{"git push *", "git status", false},
		{"npm", "npm", true},
		{"npm", "npm test", false},
		{"*", "anything at all", true},
		{"*.env", "/srv/app/.env", true},
		// '*' must span '/' — these patterns run against commands and URLs, not path segments.
		{"https://internal/*", "https://internal/a/b/c", true},
		{"rm -rf /*", "rm -rf /tmp/x", true},
		{"?at", "cat", true},
		{"?at", "chat", false},
		{"a*b*c", "azzbzzc", true},
		{"a*b*c", "azzc", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestWithinPrefixRequiresSeparatorBoundary(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	sibling := filepath.Join(dir, "repo-secrets")
	for _, d := range []string{repo, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if !withinPrefix(repo, filepath.Join(repo, "src", "main.go")) {
		t.Error("a file inside the subtree must match")
	}
	if !withinPrefix(repo, repo) {
		t.Error("the subtree root itself must match")
	}
	if withinPrefix(repo, filepath.Join(sibling, "key.pem")) {
		t.Error("SECURITY: a sibling sharing a name prefix must NOT match")
	}
	if withinPrefix(repo, "npm test") {
		t.Error("a bare command must never be treated as a path")
	}
}

// TestRulePrecedenceDenyBeatsAllow: a narrow deny must win over a broad allow no matter the file order.
func TestRulePrecedenceDenyBeatsAllow(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	h.addApprovalRule(ApprovalRule{Provider: "opencode", Tool: "bash", Action: protocol.DecisionAllow})
	h.addApprovalRule(ApprovalRule{Tool: "bash", Pattern: "git push *", Action: "deny"})

	if d, ok := h.evaluateApproval("opencode", "", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "bash", Detail: "git status"}); !ok || d != protocol.DecisionAllow {
		t.Errorf("ordinary bash should be allowed, got %q/%v", d, ok)
	}
	if d, ok := h.evaluateApproval("opencode", "", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "bash", Detail: "git push origin main"}); !ok || d != "deny" {
		t.Errorf("deny rule must beat the broad allow, got %q/%v", d, ok)
	}
}

func TestRuleScoping(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))
	h.addApprovalRule(ApprovalRule{Tool: "bash", Pattern: "npm *", ProjectID: "proj-a", Action: protocol.DecisionAllow})

	if _, ok := h.evaluateApproval("opencode", "proj-a", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "bash", Detail: "npm test"}); !ok {
		t.Error("matching project + pattern should match")
	}
	if _, ok := h.evaluateApproval("opencode", "proj-b", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "bash", Detail: "npm test"}); ok {
		t.Error("a project-scoped rule must not leak into another project")
	}
	if _, ok := h.evaluateApproval("opencode", "proj-a", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "bash", Detail: "rm -rf /"}); ok {
		t.Error("pattern must gate the command")
	}
	if _, ok := h.evaluateApproval("opencode", "proj-a", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "edit", Detail: "npm test"}); ok {
		t.Error("tool must be gated")
	}
}

// TestLegacyRulesMigrate: a v1 []string file loads, keeps working, and is rewritten in the new shape.
func TestLegacyRulesMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`["opencode|bash","claude-code|Edit"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New()
	h.SetApprovalRulesPath(path)

	if d, ok := h.evaluateApproval("opencode", "", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "bash", Detail: "anything"}); !ok || d != protocol.DecisionAllow {
		t.Fatal("legacy rule stopped working after migration")
	}
	if _, ok := h.evaluateApproval("opencode", "", protocol.ExecKindLocal, protocol.ApprovalRequest{Tool: "Edit"}); ok {
		t.Error("legacy rule must stay scoped to its own provider")
	}
	// The file must now be the structured format.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rules []ApprovalRule
	if err := json.Unmarshal(b, &rules); err != nil || len(rules) != 2 {
		t.Fatalf("file was not migrated to structured rules: %v (%s)", err, b)
	}
}

func TestCommandPrefixGlob(t *testing.T) {
	cases := map[string]string{
		"git commit -m 'x'":     "git commit *",
		"npm test":              "npm test *",
		"ls":                    "ls",
		"cat foo.txt":           "cat *",
		"/usr/bin/make build":   "make *",
		"git -C /x status":      "git *", // leading flag → don't invent a subcommand
		"rm -rf / && curl x":    "",      // shell metacharacters: never generalize
		"echo hi > /etc/passwd": "",
	}
	for in, want := range cases {
		if got := commandPrefixGlob(in); got != want {
			t.Errorf("commandPrefixGlob(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSuggestScopesPrefersHarnessPatterns: opencode's own globs beat our command parsing.
func TestSuggestScopesPrefersHarnessPatterns(t *testing.T) {
	ar := protocol.ApprovalRequest{Tool: "bash", Detail: "git push origin main"}
	got := suggestScopes(ar, []string{"git push *"}, "proj")
	if len(got) == 0 || got[0].Kind != "pattern" || got[0].Value != "git push *" {
		t.Fatalf("harness patterns must be offered first, got %+v", got)
	}
	// Always ends with the broad fallbacks so the user can still pick "this tool everywhere".
	last := got[len(got)-1]
	if last.Kind != "project" {
		t.Errorf("a project-scoped option should be offered when a project is known, got %+v", got)
	}
	var hasTool bool
	for _, s := range got {
		if s.Kind == "tool" {
			hasTool = true
		}
	}
	if !hasTool {
		t.Error("the plain per-tool scope must always be offered")
	}
}

// A rule persisted while working LOCALLY must not authorize the same action on a remote host.
//
// This is the asymmetry that makes an unset ExecScope mean "local" rather than "any": every rule
// written before the field existed was a user answering a prompt about a specific machine, and
// reading those as "anywhere" would hand a build box standing permission nobody granted.
func TestLegacyRuleDoesNotAuthorizeRemote(t *testing.T) {
	h := New()
	h.approvalRules = []ApprovalRule{
		{Provider: "opencode", Tool: "bash", Pattern: "rm *", Action: protocol.DecisionAllow}, // no ExecScope: legacy
	}
	ar := protocol.ApprovalRequest{Tool: "bash", Detail: "rm -rf build"}

	if d, ok := h.evaluateApproval("opencode", "", protocol.ExecKindLocal, ar); !ok || d != protocol.DecisionAllow {
		t.Fatalf("local should still auto-allow: decision=%q matched=%v", d, ok)
	}
	if _, ok := h.evaluateApproval("opencode", "", protocol.ExecKindSSH, ar); ok {
		t.Fatal("a legacy local rule must NOT auto-approve over ssh — the user is re-asked instead")
	}
}

// The three explicit scopes each mean what they say.
func TestExecScopeMatching(t *testing.T) {
	ar := protocol.ApprovalRequest{Tool: "bash", Detail: "npm test"}
	for _, tc := range []struct {
		scope              string
		wantLocal, wantSSH bool
	}{
		{ExecScopeLocal, true, false},
		{ExecScopeRemote, false, true},
		{ExecScopeAny, true, true},
		{"", true, false}, // legacy
	} {
		h := New()
		h.approvalRules = []ApprovalRule{
			{Provider: "opencode", Tool: "bash", Pattern: "npm *", ExecScope: tc.scope, Action: protocol.DecisionAllow},
		}
		if _, ok := h.evaluateApproval("opencode", "", protocol.ExecKindLocal, ar); ok != tc.wantLocal {
			t.Errorf("scope %q local: matched=%v want %v", tc.scope, ok, tc.wantLocal)
		}
		if _, ok := h.evaluateApproval("opencode", "", protocol.ExecKindSSH, ar); ok != tc.wantSSH {
			t.Errorf("scope %q ssh: matched=%v want %v", tc.scope, ok, tc.wantSSH)
		}
	}
}

// A DENY must not be narrowed by the same defaulting — a legacy deny that stopped applying the
// moment work moved to a remote host would be a security regression, not a convenience.
func TestDenyStillAppliesRemotely(t *testing.T) {
	h := New()
	h.approvalRules = []ApprovalRule{
		{Provider: "opencode", Tool: "bash", Pattern: "git push *", ExecScope: ExecScopeAny, Action: "deny"},
	}
	ar := protocol.ApprovalRequest{Tool: "bash", Detail: "git push origin main"}
	for _, kind := range []string{protocol.ExecKindLocal, protocol.ExecKindSSH} {
		if d, ok := h.evaluateApproval("opencode", "", kind, ar); !ok || d != "deny" {
			t.Errorf("exec kind %q: decision=%q matched=%v, want a deny", kind, d, ok)
		}
	}
}

// A rule minted from a user's ALWAYS answer inherits the location they were answering about.
func TestRuleFromDecisionInheritsLocation(t *testing.T) {
	for kind, want := range map[string]string{
		protocol.ExecKindLocal: ExecScopeLocal,
		protocol.ExecKindSSH:   ExecScopeRemote,
	} {
		p := pendingApproval{
			req:      protocol.ApprovalRequest{Tool: "bash", Detail: "npm test"},
			provider: "opencode",
			execKind: kind,
		}
		if got := ruleFromDecision(p, nil); got.ExecScope != want {
			t.Errorf("exec kind %q produced scope %q, want %q", kind, got.ExecScope, want)
		}
	}
}
