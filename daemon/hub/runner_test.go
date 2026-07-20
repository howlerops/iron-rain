package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectTestCommand(t *testing.T) {
	cases := []struct {
		marker string
		want   string
	}{
		{"go.mod", "go test ./..."},
		{"Cargo.toml", "cargo test"},
		{"package.json", "npm test"},
		{"pyproject.toml", "pytest"},
		{"Package.swift", "swift test"},
		{"Makefile", "make test"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, c.marker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(detectTestCommand(dir), " ")
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s → %q, want prefix %q", c.marker, got, c.want)
		}
	}
	// Unknown project → nil.
	if got := detectTestCommand(t.TempDir()); got != nil {
		t.Errorf("empty dir → %v, want nil", got)
	}
}
