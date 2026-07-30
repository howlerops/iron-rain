package mcp

import "testing"

// TestRuntimeFor: only unambiguous runtimes are mapped. A wrong guess here would execute an
// arbitrary third-party command, so an unknown packaging must be reported, never improvised.
func TestRuntimeFor(t *testing.T) {
	cmd, args, ok := runtimeFor("npm", "@modelcontextprotocol/server-github", "1.2.3")
	if !ok || cmd != "npx" || args[0] != "-y" || args[1] != "@modelcontextprotocol/server-github@1.2.3" {
		t.Errorf("npm mapping = %q %v %v", cmd, args, ok)
	}
	// A version already pinned in the identifier isn't doubled up.
	_, args, _ = runtimeFor("npm", "pkg@2.0.0", "3.0.0")
	if args[1] != "pkg@2.0.0" {
		t.Errorf("an identifier that already pins a version must be left alone, got %v", args)
	}
	if cmd, _, ok := runtimeFor("pypi", "mcp-server-git", ""); !ok || cmd != "uvx" {
		t.Errorf("pypi should map to uvx, got %q/%v", cmd, ok)
	}
	if cmd, _, ok := runtimeFor("oci", "ghcr.io/x/y:1", ""); !ok || cmd != "docker" {
		t.Errorf("oci should map to docker, got %q/%v", cmd, ok)
	}
	for _, unknown := range []string{"cargo", "gem", "", "nix"} {
		if _, _, ok := runtimeFor(unknown, "x", ""); ok {
			t.Errorf("runtime %q must NOT be guessed at", unknown)
		}
	}
}

func TestClipBoundsUntrustedText(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'a'
	}
	if got := clip(string(long), 120); len(got) > 124 {
		t.Errorf("registry text must be bounded, got %d chars", len(got))
	}
	if clip("  hi  ", 10) != "hi" {
		t.Error("clip should trim")
	}
}
