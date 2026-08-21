package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Live coverage for the two languages that had routing and install recipes but no end-to-end test.
//
// The gap mattered most for Go: the daemon is itself a Go codebase, so the Go path is the one a user
// of this project exercises constantly, and it was the least verified. `registry.go` mapped .go to
// gopls and `install.go` knew how to install it, but nothing proved a real gopls actually completes
// the handshake and publishes diagnostics through this client.
//
// Both skip when the server isn't installed, matching the pyright tests — CI without a toolchain
// stays green, and a developer with the server gets real coverage.

// waitForErrorDiagnostic blocks until an error-severity diagnostic arrives for the opened file.
func waitForErrorDiagnostic(t *testing.T, got <-chan []Diagnostic, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case diags := <-got:
			for _, d := range diags {
				if d.Severity == 1 && d.Message != "" {
					return
				}
			}
		case <-deadline:
			t.Fatal("no error diagnostic published within timeout")
		}
	}
}

// openAndWait spins up a Manager against one file and asserts the server reports a real error.
func openAndWait(t *testing.T, dir, file, src string, within time.Duration) {
	t.Helper()
	got := make(chan []Diagnostic, 8)
	m := NewManager(func(path string, diags []Diagnostic) {
		if path == file {
			got <- diags
		}
	})
	defer m.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	if err := m.Open(ctx, file, src); err != nil {
		t.Fatalf("open: %v", err)
	}
	waitForErrorDiagnostic(t, got, within)
}

// TestLive_GoplsDiagnostics exercises the full Go path against a real gopls.
//
// gopls needs a module to analyze a file at all — handed a bare .go file with no go.mod it reports
// nothing rather than erroring, which would make this test pass vacuously if the module were
// omitted. So the temp dir is a real module.
func TestLive_GoplsDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module lsptest\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "bad.go")
	// A type error, not a syntax error: it proves gopls actually TYPE-CHECKED the file rather than
	// merely parsing it, which is the capability that makes the Go path worth having.
	src := "package main\n\nfunc main() {\n\tvar n int = \"not an int\"\n\t_ = n\n}\n"
	openAndWait(t, dir, file, src, 60*time.Second)
}

// TestLive_TypeScriptDiagnostics exercises the TypeScript path against a real
// typescript-language-server.
func TestLive_TypeScriptDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("typescript-language-server"); err != nil {
		t.Skip("typescript-language-server not installed")
	}
	dir := t.TempDir()
	// tsconfig.json is what makes the server treat the directory as a project; without it the
	// server may run in a degraded single-file mode and skip the semantic check we care about.
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"compilerOptions":{"strict":true,"noEmit":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "bad.ts")
	src := "const n: number = \"not a number\";\nexport default n;\n"
	openAndWait(t, dir, file, src, 60*time.Second)
}

// Both languages must resolve to a server and be reported installable, since the editor's
// install affordance is driven entirely by this. A regression here is silent: the file simply
// never gets diagnostics and nothing says why.
func TestInfoForPath_GoAndTypeScript(t *testing.T) {
	for _, tc := range []struct{ file, lang, binary string }{
		{"main.go", "go", "gopls"},
		{"app.ts", "typescript", "typescript-language-server"},
		{"app.tsx", "typescript", "typescript-language-server"},
		{"app.js", "javascript", "typescript-language-server"},
	} {
		info := InfoForPath(tc.file)
		if info.Language != tc.lang {
			t.Errorf("%s: language = %q, want %q", tc.file, info.Language, tc.lang)
		}
		if info.Binary != tc.binary {
			t.Errorf("%s: binary = %q, want %q", tc.file, info.Binary, tc.binary)
		}
		// Installed or not depends on the machine, but a recipe must always exist — otherwise the UI
		// can only say "unsupported" for a language we do in fact support.
		if !info.Installed && !info.Installable && info.InstallLabel == "" {
			t.Errorf("%s: neither installed nor installable and no label — the user is told nothing", tc.file)
		}
	}
}
