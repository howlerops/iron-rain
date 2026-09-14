package hub

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/protocol"
)

// The config opencode is handed must point at the gateway, not at the real server.
//
// The ordering guard in the main package stops the base from being unset when this is rendered; this
// is the other half — that with the base set, what comes out is a gateway URL and a bearer token,
// and NOT the server's command line or the credentials in its environment.
func TestOpenCodesInjectedConfigRoutesThroughTheGateway(t *testing.T) {
	h := New()
	r := mcp.NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	if err := r.Upsert(mcp.Server{
		Name: "github", Transport: "stdio",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"},
		Env: map[string]string{"GITHUB_TOKEN": "ghp_realsecret"},
	}); err != nil {
		t.Fatal(err)
	}
	h.SetMCPRegistry(r)
	h.SetMCPGateway(mcp.NewGateway(mcp.NewManager(r), "tok-123"), "tok-123")
	h.SetMCPGatewayBase("http://127.0.0.1:6000")

	cfg := h.OpenCodeMCPConfig()
	if cfg == "" {
		t.Fatal("no config was produced")
	}
	if strings.Contains(cfg, "ghp_realsecret") {
		t.Errorf("the server's credential is in the config handed to opencode:\n%s\n\nThe harness "+
			"spawns its own copy of the server with this in its environment, where any agent bash "+
			"step can read it out of `ps eww`.", cfg)
	}
	if strings.Contains(cfg, "@modelcontextprotocol/server-github") {
		t.Errorf("the raw command line is in the config handed to opencode:\n%s\n\nopencode will run "+
			"the server itself, so its tool calls never transit the daemon and never reach "+
			"authorizeMCPTool — no mode check, no approval rule, no approval card.", cfg)
	}
	if !strings.Contains(cfg, "127.0.0.1:6000") {
		t.Errorf("the config does not point at the gateway:\n%s", cfg)
	}
	// A bearer token, but NOT the machine-wide one. authorizeMCPTool treats the machine token as
	// "the user's own tooling" and lets it past the mode gate, the rule engine and the approval card
	// — so handing it to an agent harness made every opencode session's MCP calls unapprovable. The
	// harness token is accepted by the gateway and bound to no session, which routes it to an
	// approval the user actually answers.
	if strings.Contains(cfg, "tok-123") {
		t.Errorf("the config carries the MACHINE-WIDE token:\n%s\n\nauthorizeMCPTool allows that "+
			"token unconditionally, so an Ask/Architect session — which the UI calls read-only — "+
			"could call a mutating MCP tool with no card and no standing rule applied.", cfg)
	}
	if !strings.Contains(cfg, "Bearer mcph_") {
		t.Errorf("the config carries no harness bearer token, so the gateway will refuse it:\n%s", cfg)
	}
}

// The harness token must be authorized by the gateway but attributable to NO session — that pairing
// is what routes its calls to askUnattributedMCPApproval instead of the machine-token allowance.
func TestHarnessTokenIsAcceptedButBoundToNoSession(t *testing.T) {
	h := New()
	r := mcp.NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	h.SetMCPRegistry(r)
	h.SetMCPGateway(mcp.NewGateway(mcp.NewManager(r), "tok-123"), "tok-123")

	h.mu.Lock()
	harness := h.mcpHarnessToken
	h.mu.Unlock()
	if harness == "" {
		t.Fatal("no harness token was minted, so OpenCodeMCPConfig falls back to the machine token")
	}
	if harness == "tok-123" {
		t.Fatal("the harness token IS the machine token — the allowance it was meant to avoid")
	}
	if _, ok := h.mcpTokens.session(harness); ok {
		t.Fatal("the harness token resolves to a session; it must not, or it would inherit that " +
			"session's rules while belonging to every opencode session at once")
	}
}

// A harness-token call must WAIT for a human, not sail through.
//
// This is the whole point of the harness token. With the machine token in the injected config,
// authorizeMCPTool hit the "no session context" early return and allowed every opencode MCP tool
// call: no mode gate, no standing rule, no card. A read-only session could write through an MCP
// server, and a standing deny rule was inert.
func TestAHarnessTokenCallWaitsForApproval(t *testing.T) {
	h := New()
	r := mcp.NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	h.SetMCPRegistry(r)
	h.SetMCPGateway(mcp.NewGateway(mcp.NewManager(r), "tok-123"), "tok-123")
	h.mu.Lock()
	harness := h.mcpHarnessToken
	h.mu.Unlock()

	// Nobody is connected to answer, so the call must end unanswered rather than permitted.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := h.authorizeMCPTool(ctx, harness, "github", "create_issue", nil)
	if err == nil {
		t.Fatal("an unattributed harness call was ALLOWED with nobody having approved it.\n\n" +
			"Every opencode session shares this token, so this is every MCP tool call opencode " +
			"makes: past the mode gate, past standing rules, and with no card on any device.")
	}

	// The machine token keeps its allowance: that path is the user's own tooling, not an agent.
	if err := h.authorizeMCPTool(ctx, "tok-123", "github", "create_issue", nil); err != nil {
		t.Fatalf("the machine-wide token was refused, which breaks the user's own tooling: %v", err)
	}
}

// Answering the card releases the blocked call — the card must not be decorative.
func TestAnsweringAnUnattributedApprovalReleasesTheCall(t *testing.T) {
	h := New()
	r := mcp.NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	h.SetMCPRegistry(r)
	h.SetMCPGateway(mcp.NewGateway(mcp.NewManager(r), "tok-123"), "tok-123")
	h.mu.Lock()
	harness := h.mcpHarnessToken
	h.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		result <- h.authorizeMCPTool(context.Background(), harness, "github", "create_issue", nil)
	}()

	// Find the pending approval the call raised, then answer it the way approval.respond does.
	var id string
	for i := 0; i < 200 && id == ""; i++ {
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
		t.Fatal("the call raised no approval waiter, so no card was ever offered")
	}
	if !h.resolveMCPApproval(id, protocol.DecisionAllow) {
		t.Fatal("the waiter could not be resolved, so answering the card would report 'no such approval'")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("an APPROVED call was still refused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approving the card did not release the blocked MCP call")
	}
}

// An http-transport server must be fronted by the gateway too, not handed to the harness raw.
//
// It used to be passed through untouched — real vendor URL, and the stored Headers, which is where
// an API key lives. The harness then connected straight to the vendor, so its tool calls never
// reached Gateway.ServeHTTP: no mode gate, no rule evaluation, no approval card, no audit line, and
// the .git guard never saw them. The credential was also copied into the harness's config file,
// where any agent bash step can read it.
//
// Never a capability limit: Manager.Dial already proxies hosted servers.
func TestAHostedServerIsFrontedByTheGatewayToo(t *testing.T) {
	dir := t.TempDir()
	r := mcp.NewRegistry(filepath.Join(dir, "mcp.json"))
	if err := r.Upsert(mcp.Server{
		Name: "vendor", Transport: "http",
		URL:     "https://api.vendor.example/mcp",
		Headers: map[string]string{"Authorization": "Bearer sk-vendor-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	h := New()
	h.SetMCPRegistry(r)
	h.SetMCPGateway(mcp.NewGateway(mcp.NewManager(r), "tok-123"), "tok-123")
	h.SetMCPGatewayBase("http://127.0.0.1:6000")

	out := h.gatewayServers(r.List(), "sess-token")
	if len(out) != 1 {
		t.Fatalf("got %d servers, want 1", len(out))
	}
	got := out[0]
	if got.URL != "http://127.0.0.1:6000/mcp/vendor" {
		t.Errorf("the harness was pointed at %q, not at the gateway — its tool calls never reach "+
			"authorizeMCPTool, so no mode, rule, card or audit line applies to them", got.URL)
	}
	if got.Headers["Authorization"] == "Bearer sk-vendor-secret" {
		t.Error("the vendor credential was copied into the harness config, where any agent bash " +
			"step can read it out of the file or the process environment")
	}
	if got.Headers["Authorization"] != "Bearer sess-token" {
		t.Errorf("the gateway bearer is wrong: %q", got.Headers["Authorization"])
	}
}
