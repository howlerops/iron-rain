package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
