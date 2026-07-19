package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLive_PyrightSymbolsAndReferences exercises DocumentSymbols + References against a real
// pyright server (skips if not installed). It opens a file with a function definition and a
// call site, then asserts the outline contains the function and references finds both sites.
func TestLive_PyrightSymbolsAndReferences(t *testing.T) {
	if _, err := exec.LookPath("pyright-langserver"); err != nil {
		t.Skip("pyright-langserver not installed")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.pyright]\n"), 0o644)
	file := filepath.Join(dir, "m.py")
	src := "def greet(name):\n    return name\n\n\nprint(greet(\"a\"))\n"
	_ = os.WriteFile(file, []byte(src), 0o644)

	m := NewManager(func(_ string, _ []Diagnostic) {})
	defer m.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := m.Open(ctx, file, src); err != nil {
		t.Fatalf("open: %v", err)
	}

	// Symbols: retry until pyright has analyzed the file.
	var haveGreet bool
	for i := 0; i < 20 && !haveGreet; i++ {
		syms, _ := m.DocumentSymbols(ctx, file)
		haveGreet = containsSymbol(syms, "greet")
		if !haveGreet {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !haveGreet {
		t.Fatal("DocumentSymbols never returned the 'greet' function")
	}

	// References at the definition of greet (line 0, char 4 = the name). Expect def + call.
	var refs []Location
	for i := 0; i < 20; i++ {
		refs, _ = m.References(ctx, file, 0, 4)
		if len(refs) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(refs) < 2 {
		t.Fatalf("References found %d locations, want >= 2 (def + call)", len(refs))
	}
}

func containsSymbol(syms []Symbol, name string) bool {
	for _, s := range syms {
		if s.Name == name || containsSymbol(s.Children, name) {
			return true
		}
	}
	return false
}
