package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeServer(t *testing.T, mode string) Server {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	abs, err := filepath.Abs(filepath.Join("testdata", "fake_server.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return Server{
		Name: "fake", Transport: "stdio", Command: "node", Args: []string{abs},
		Env: map[string]string{"FAKE_MCP_MODE": mode},
	}
}

// TestProbeLegacyProtocol: the pre-2026 initialize handshake, which is what ~every published server
// still speaks. server/discover fails first and we must fall back cleanly.
func TestProbeLegacyProtocol(t *testing.T) {
	s := fakeServer(t, "legacy")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, s.Command, s.Args, s.Env, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	info, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v (stderr: %s)", err, c.Stderr())
	}
	if info.ProtocolVersion != ProtocolLegacy {
		t.Errorf("protocol = %q, want %q", info.ProtocolVersion, ProtocolLegacy)
	}
	if info.Name != "fake" || len(info.Tools) != 2 {
		t.Errorf("unexpected server info %+v", info)
	}
}

// TestProbeLatestProtocol: the 2026-07-28 stateless shape (server/discover, no initialize).
func TestProbeLatestProtocol(t *testing.T) {
	s := fakeServer(t, "latest")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, s.Command, s.Args, s.Env, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	info, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v (stderr: %s)", err, c.Stderr())
	}
	if info.ProtocolVersion != ProtocolLatest {
		t.Errorf("protocol = %q, want %q", info.ProtocolVersion, ProtocolLatest)
	}
	if len(info.Tools) != 2 {
		t.Errorf("tools = %+v, want 2", info.Tools)
	}
}

// TestCheckReportsFailureWithStderr: a broken server must explain itself. Its stderr is the only
// clue a user ever gets ("MODULE_NOT_FOUND", "missing API key").
func TestCheckReportsFailureWithStderr(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "mcp.json"))
	if err := r.Upsert(Server{
		Name: "broken", Transport: "stdio", Command: "sh",
		Args: []string{"-c", "echo 'boom: missing API key' >&2; exit 1"},
	}); err != nil {
		t.Fatal(err)
	}
	st := r.Check(context.Background(), "broken")
	if st.OK {
		t.Fatal("a failing server must not report OK")
	}
	if st.Error == "" {
		t.Fatal("a failure must carry an explanation")
	}
	if !containsSub(st.Error, "missing API key") {
		t.Errorf("error should surface the server's stderr, got %q", st.Error)
	}
}

func containsSub(s, sub string) bool { return strings.Contains(s, sub) }

// TestRegistryRoundTripAndScoping covers persistence and project scoping together.
func TestRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp.json")
	r := NewRegistry(path)
	if err := r.Upsert(Server{Name: "b", Command: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(Server{Name: "a", Command: "a", ProjectID: "proj-1"}); err != nil {
		t.Fatal(err)
	}
	// Sorted by name, transport defaulted.
	list := r.List()
	if len(list) != 2 || list[0].Name != "a" || list[0].Transport != "stdio" {
		t.Fatalf("unexpected list %+v", list)
	}
	// The file must be 0600 — Env holds credentials.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("registry file mode = %o, want 600", perm)
	}
	// Scoping: an unscoped server is everywhere; a scoped one only in its project.
	if got := r.Enabled("proj-1"); len(got) != 2 {
		t.Errorf("proj-1 should see both servers, got %d", len(got))
	}
	if got := r.Enabled("proj-2"); len(got) != 1 || got[0].Name != "b" {
		t.Errorf("proj-2 should see only the unscoped server, got %+v", got)
	}
	// Disabled servers are never injected.
	if err := r.SetEnabled("b", false); err != nil {
		t.Fatal(err)
	}
	if got := r.Enabled("proj-2"); len(got) != 0 {
		t.Errorf("a disabled server must not be injected, got %+v", got)
	}
	// Survives a reload.
	r2 := NewRegistry(path)
	if len(r2.List()) != 2 {
		t.Error("registry did not survive reload")
	}
	if got := r2.Enabled("proj-2"); len(got) != 0 {
		t.Error("disabled state did not survive reload")
	}
	if err := r2.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if len(NewRegistry(path).List()) != 1 {
		t.Error("delete did not persist")
	}
}

// TestInjectionFormats locks each harness's documented config shape. These differ in ways that are
// easy to get subtly wrong — opencode takes command as an ARRAY and calls the env map "environment".
func TestInjectionFormats(t *testing.T) {
	servers := []Server{{
		Name: "gh", Transport: "stdio", Command: "npx", Args: []string{"-y", "gh-mcp"},
		Env: map[string]string{"TOKEN": "t"},
	}}

	var claude map[string]any
	if err := json.Unmarshal([]byte(ClaudeConfigJSON(servers)), &claude); err != nil {
		t.Fatal(err)
	}
	gh := claude["gh"].(map[string]any)
	if gh["type"] != "stdio" || gh["command"] != "npx" {
		t.Errorf("claude shape wrong: %+v", gh)
	}
	if _, ok := gh["env"].(map[string]any); !ok {
		t.Errorf("claude env should be an object: %+v", gh)
	}

	var oc map[string]any
	if err := json.Unmarshal([]byte(OpenCodeConfigJSON(servers)), &oc); err != nil {
		t.Fatal(err)
	}
	entry := oc["mcp"].(map[string]any)["gh"].(map[string]any)
	if entry["type"] != "local" {
		t.Errorf("opencode uses type=local, got %v", entry["type"])
	}
	cmd, ok := entry["command"].([]any)
	if !ok || len(cmd) != 3 || cmd[0] != "npx" {
		t.Errorf("opencode command must be the full argv ARRAY, got %v", entry["command"])
	}
	if _, ok := entry["environment"]; !ok {
		t.Errorf("opencode names the env map 'environment', got keys %v", entry)
	}
	if _, ok := entry["env"]; ok {
		t.Errorf("opencode must NOT use 'env' — that's the Claude spelling")
	}

	// Nothing configured → nothing injected, so callers can skip setting the variable entirely.
	if ClaudeConfigJSON(nil) != "" || OpenCodeConfigJSON(nil) != "" || WriteCLIConfig(nil) != "" {
		t.Error("an empty server list must render as empty, not an empty document")
	}

	// The CLI file is a real 0600 file with an mcpServers document.
	path := WriteCLIConfig(servers)
	if path == "" {
		t.Fatal("expected a config file")
	}
	defer os.Remove(path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("cli config mode = %o, want 600 (it holds credentials)", perm)
	}
	b, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["mcpServers"]; !ok {
		t.Errorf("cli config must use the mcpServers key, got %v", doc)
	}
}
