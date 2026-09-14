package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gateway authorized tools/call and nothing else.
//
// resources/read and prompts/get are ordinary MCP methods that both claude-code and opencode issue,
// and each carries a caller-supplied selector. Only tools/call ran the Authorizer, so an agent could
// ask a server for a resource by URI and face no mode check, no approval rule, no approval card and
// no .git refusal — while the Authorizer's own doc comment describes it as "the seam that lets MCP
// tools obey the same approval rules and read-only modes as native tools".
//
// Two halves had to be fixed for this to bite: the gateway has to CALL the authorizer for the method,
// and the guard has to understand `uri` (and the file:// scheme it arrives wrapped in), or it inspects
// a string that matches nothing.
func TestAResourceReadIsGuardedLikeAFileRead(t *testing.T) {
	h := New()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]string{"uri": "file://" + filepath.Join(repo, ".git", "config")})
	err := h.authorizeMCPTool(context.Background(), "machine-token", "files", "resources/read", args)
	if err == nil {
		t.Error("a resources/read of .git/config was allowed. Repository metadata is refused because " +
			"of what it IS — a hook placed there is executed by the daemon's own git commit when the " +
			"session finishes — and that reasoning does not depend on which JSON-RPC method asked.")
	}
}

// The guard has to see through the URI scheme. Without this the path comparison runs against
// "file:///Users/..." and matches nothing, so the method would be authorized and still wave it past.
func TestAFileURIIsUnwrappedBeforeThePathIsJudged(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///Users/jacob/.ssh/id_ed25519", "/Users/jacob/.ssh/id_ed25519"},
		{"file://localhost/Users/jacob/x", "/Users/jacob/x"},
		{"/Users/jacob/plain", "/Users/jacob/plain"},
		{"https://example.com/x", "https://example.com/x"}, // not a file; left alone
	}
	for _, tc := range cases {
		if got := unwrapFileURI(tc.in); got != tc.want {
			t.Errorf("unwrapFileURI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// And the other direction: an ordinary resource read must still go through, or the MCP resource
// feature is simply broken rather than guarded.
func TestAnOrdinaryResourceReadIsStillAllowed(t *testing.T) {
	h := New()
	repo := t.TempDir()
	args, _ := json.Marshal(map[string]string{"uri": "file://" + filepath.Join(repo, "README.md")})
	if err := h.authorizeMCPTool(context.Background(), "machine-token", "files", "resources/read", args); err != nil {
		t.Errorf("an ordinary resource read was refused: %v", err)
	}
}

// The method list is the contract. Discovery methods name nothing and act on nothing, so gating them
// would cost a round trip per call for no benefit; the three that carry a selector must be gated.
func TestTheAuthorizedMethodListCoversEverySelectorCarryingMethod(t *testing.T) {
	src, err := os.ReadFile("../mcp/gateway.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "var authorizedMethods = map[string]bool{")
	if start < 0 {
		t.Fatal("authorizedMethods is gone — the gateway is no longer gating by method at all")
	}
	block := body[start : start+strings.Index(body[start:], "}")]
	for _, m := range []string{"tools/call", "resources/read", "prompts/get"} {
		if !strings.Contains(block, `"`+m+`"`) {
			t.Errorf("%s is not authorized. It carries a caller-supplied selector, so it reaches the "+
				"server's content without passing modes, rules, approvals or the .git guard.", m)
		}
	}
}
