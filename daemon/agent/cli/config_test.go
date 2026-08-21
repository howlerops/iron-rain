package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// An AG-UI entry is a valid custom agent even though it has no Command — it names an endpoint the
// daemon POSTs runs to instead of a process it spawns.
func TestLoadAcceptsAGUIEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	err := os.WriteFile(path, []byte(`[
	  {"name":"my-agent","endpoint":"https://example.test/agui","headers":{"Authorization":"Bearer x"}},
	  {"name":"a-cli","command":"foo","args":["-p","{prompt}"]},
	  {"name":"neither"},
	  {"name":"both","command":"foo","endpoint":"https://example.test/agui"}
	]`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// "neither" is unusable; "both" is ambiguous about which transport to drive. Guessing at either
	// would mean silently running something the user did not describe.
	if len(got) != 2 {
		t.Fatalf("expected the two well-formed entries, got %+v", got)
	}
	if !got[0].IsAGUI() || got[0].Endpoint != "https://example.test/agui" {
		t.Errorf("first entry should be an AG-UI backend: %+v", got[0])
	}
	if got[0].Headers["Authorization"] != "Bearer x" {
		t.Errorf("headers should survive the round trip: %+v", got[0].Headers)
	}
	if got[1].IsAGUI() {
		t.Errorf("a command-based entry must not be treated as AG-UI: %+v", got[1])
	}
}
