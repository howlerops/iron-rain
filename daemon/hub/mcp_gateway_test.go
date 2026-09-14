package hub

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/mcp"
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
	if !strings.Contains(cfg, "tok-123") {
		t.Errorf("the config carries no bearer token, so the gateway will refuse it:\n%s", cfg)
	}
}
