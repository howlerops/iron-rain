package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent/claudecode"
)

func TestParseSetupMode(t *testing.T) {
	cases := map[string]setupMode{
		"auto": setupAuto, "yes": setupAuto,
		"off": setupOff, "no": setupOff, "false": setupOff,
		"ask": setupAsk, "": setupAsk, "garbage": setupAsk, "AUTO": setupAuto,
	}
	for in, want := range cases {
		if got := parseSetupMode(in); got != want {
			t.Errorf("parseSetupMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMaterializeSidecar(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claude-sidecar")
	if err := materializeSidecar(dir); err != nil {
		t.Fatal(err)
	}
	mjs, err := os.ReadFile(filepath.Join(dir, "sidecar.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mjs) == 0 || len(claudecode.SidecarMJS) == 0 || string(mjs) != string(claudecode.SidecarMJS) {
		t.Fatal("sidecar.mjs not materialized from the embedded copy")
	}
	pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pkg) != string(claudecode.SidecarPackageJSON) {
		t.Fatal("package.json not materialized from the embedded copy")
	}
	// Idempotent: a second call (upgrade refresh) must not error.
	if err := materializeSidecar(dir); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
}

func TestFirstUsableSidecar(t *testing.T) {
	dir := t.TempDir()

	// A candidate whose sidecar.mjs exists but has NO node_modules -> not usable.
	bare := filepath.Join(dir, "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	bareMjs := filepath.Join(bare, "sidecar.mjs")
	if err := os.WriteFile(bareMjs, []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A candidate that is fully installed (sidecar.mjs + node_modules/) -> usable.
	good := filepath.Join(dir, "good")
	if err := os.MkdirAll(filepath.Join(good, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	goodMjs := filepath.Join(good, "sidecar.mjs")
	if err := os.WriteFile(goodMjs, []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(dir, "nope", "sidecar.mjs")

	// Empty/missing/bare all skipped; the installed one wins even when listed last.
	got := firstUsableSidecar([]string{"", missing, bareMjs, goodMjs})
	if got != goodMjs {
		t.Fatalf("firstUsableSidecar = %q, want %q", got, goodMjs)
	}

	// Nothing usable -> "".
	if got := firstUsableSidecar([]string{"", missing, bareMjs}); got != "" {
		t.Fatalf("firstUsableSidecar (none installed) = %q, want empty", got)
	}
}

// TestOpenCodePortStickiness verifies the daemon remembers the opencode server IT started and hands
// back that same URL on a later run — so restarts reconnect to the daemon's OWN server (where its
// sessions live) instead of drifting onto the user's other opencode instances.
func TestOpenCodePortStickiness(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.oculus/opencode-port from the real home

	if url := rememberedOpenCodeURL(); url != "" {
		t.Fatalf("expected no remembered server initially, got %q", url)
	}
	rememberOpenCodePort(49596)
	if got := rememberedOpenCodeURL(); got != "http://127.0.0.1:49596" {
		t.Fatalf("remembered URL = %q, want http://127.0.0.1:49596", got)
	}
	// A garbage port file must be ignored, not returned as a bogus URL.
	_ = os.WriteFile(opencodePortFile(), []byte("not-a-port"), 0o600)
	if got := rememberedOpenCodeURL(); got != "" {
		t.Fatalf("garbage port file should yield no URL, got %q", got)
	}
}
