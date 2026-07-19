package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLive_PyrightDiagnostics is an opt-in end-to-end test against a real pyright server. It
// skips if pyright-langserver isn't installed. It opens a Python file with a syntax error and
// asserts an error diagnostic is published through the Manager's callback — exercising the
// full path: server spawn, initialize handshake, didOpen, and publishDiagnostics parsing.
func TestLive_PyrightDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("pyright-langserver"); err != nil {
		t.Skip("pyright-langserver not installed")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.pyright]\n"), 0o644)
	file := filepath.Join(dir, "bad.py")
	src := "def f(:\n    return 1\n" // deliberate syntax error

	got := make(chan []Diagnostic, 8)
	m := NewManager(func(path string, diags []Diagnostic) {
		if path == file {
			got <- diags
		}
	})
	defer m.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Open(ctx, file, src); err != nil {
		t.Fatalf("open: %v", err)
	}

	deadline := time.After(40 * time.Second)
	for {
		select {
		case diags := <-got:
			for _, d := range diags {
				if d.Severity == 1 && d.Message != "" {
					return // got a real error diagnostic — the whole LSP path works
				}
			}
		case <-deadline:
			t.Fatal("no error diagnostic published within timeout")
		}
	}
}
