package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverReadsEachHarnessShape: the formats differ in ways that are easy to get wrong —
// opencode takes the full argv as an ARRAY and calls its env map "environment".
func TestDiscoverReadsEachHarnessShape(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	write(t, filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{
      "gh":{"type":"stdio","command":"npx","args":["-y","gh-mcp"],"env":{"TOKEN":"t"}},
      "hosted":{"type":"http","url":"https://mcp.example.com/mcp"}}}`)
	write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp":{
      "pg":{"type":"local","command":["uvx","mcp-server-postgres"],"environment":{"PGURL":"x"}},
      "off":{"type":"local","command":["nope"],"enabled":false}}}`)

	found := Discover(home, cwd, nil)
	byName := map[string]Found{}
	for _, f := range found {
		byName[f.Server.Name] = f
	}

	gh, ok := byName["gh"]
	if !ok || gh.Server.Command != "npx" || len(gh.Server.Args) != 2 || gh.Server.Env["TOKEN"] != "t" {
		t.Errorf("claude stdio server not parsed: %+v", gh.Server)
	}
	if h, ok := byName["hosted"]; !ok || h.Server.Transport != "http" || h.Server.URL == "" {
		t.Errorf("claude remote server not parsed: %+v", h.Server)
	}
	// opencode's array command must be split into command + args.
	pg, ok := byName["pg"]
	if !ok || pg.Server.Command != "uvx" || len(pg.Server.Args) != 1 || pg.Server.Args[0] != "mcp-server-postgres" {
		t.Errorf("opencode argv array not split correctly: %+v", pg.Server)
	}
	if pg.Server.Env["PGURL"] != "x" {
		t.Errorf("opencode names its env map 'environment'; not read: %+v", pg.Server.Env)
	}
	// A server the user already disabled must not be offered.
	if _, ok := byName["off"]; ok {
		t.Error("a disabled opencode server must not be offered for import")
	}
	// Every result says where it came from, so the user can go turn it off there.
	for _, f := range found {
		if f.Source == "" || f.Path == "" {
			t.Errorf("discovery must report its source: %+v", f)
		}
	}
}

// TestDiscoverSkipsAlreadyRegistered: importing something twice would create the duplicate this
// feature exists to prevent.
func TestDiscoverSkipsAlreadyRegistered(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"gh":{"command":"npx","args":["gh"]}}}`)

	if got := Discover(home, "", nil); len(got) != 1 {
		t.Fatalf("expected to find gh, got %+v", got)
	}
	existing := []Server{{Name: "GH"}} // case-insensitive: same server, different capitalization
	if got := Discover(home, "", existing); len(got) != 0 {
		t.Errorf("an already-registered server must not be offered again, got %+v", got)
	}
}

// TestDiscoverReadsClaudeProjectScopedServers: `claude mcp add` writes local-scope servers under
// ~/.claude.json "projects", which is where most users' servers actually live.
func TestDiscoverReadsProjectScopedClaudeServers(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{"projects":{
      "/Users/x/repo":{"mcpServers":{"scoped":{"command":"node","args":["s.js"]}}}}}`)
	found := Discover(home, "", nil)
	if len(found) != 1 || found[0].Server.Name != "scoped" {
		t.Fatalf("project-scoped servers must be discovered, got %+v", found)
	}
}

// TestDiscoverToleratesJunk: a malformed or absent config is normal, not an error.
func TestDiscoverToleratesJunk(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{not json`)
	if got := Discover(home, filepath.Join(home, "nope"), nil); len(got) != 0 {
		t.Errorf("junk config should yield nothing, got %+v", got)
	}
}
