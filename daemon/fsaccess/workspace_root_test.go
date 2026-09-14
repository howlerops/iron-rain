package fsaccess

import (
	"os"
	"path/filepath"
	"testing"
)

// A cross-repo workspace lives under ~/.oculus/workspaces, and the guard protected the whole of
// ~/.oculus except worktrees — so every isolated multi-repo session had a dead code surface.
//
// hub.go sets the session cwd to the workspace layout AFTER validateSessionCwd has already run on
// the caller's own path, so the session starts and the agent works normally. It is only the editor
// that dies: fsaccess.New drops every root as protected, and fs.tree/fs.read/fs.write/fs.search and
// all the lsp.* calls answer "refusing to touch a protected directory" for the session's lifetime.
func TestAWorkspaceLayoutIsReachable(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	layout := filepath.Join(home, ".oculus", "workspaces", "cross-repo")
	member := filepath.Join(layout, "repo-a")

	if got := ProtectedPath(member); got != "" {
		t.Errorf("ProtectedPath(%s) = %q, want \"\" — this is where the agent's checkout lives, so "+
			"the whole editor and file browser are refused for the session", member, got)
	}
	g := New([]string{layout, member})
	if len(g.Roots()) == 0 {
		t.Fatalf("New dropped every root for a workspace session — nothing under %s can be read", layout)
	}
	if _, err := g.Resolve(filepath.Join(member, "main.go")); err != nil {
		t.Errorf("Resolve inside a workspace member failed: %v", err)
	}
}

// The carve-out must stay narrow: everything else under the state directory is key material.
func TestTheRestOfTheStateDirectoryIsStillProtected(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for _, p := range []string{
		filepath.Join(home, ".oculus", "daemon.key"),
		filepath.Join(home, ".oculus", "devices.json"),
		filepath.Join(home, ".oculus", "workspaces-notreally", "x"),
	} {
		if got := ProtectedPath(p); got == "" {
			t.Errorf("ProtectedPath(%s) = \"\" — the workspaces carve-out is matching more than it should", p)
		}
	}
}
