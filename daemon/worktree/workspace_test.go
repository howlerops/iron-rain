package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateWorkspaceAndRemove(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	baseA := gitInit(t, repoA)
	baseB := gitInit(t, repoB)
	base := t.TempDir() // workspaces layout base

	layout, members, err := CreateWorkspace(base, "Cross Cut", []string{repoA, repoB}, nil)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if filepath.Base(layout) != "cross-cut" {
		t.Errorf("layout = %q, want .../cross-cut", layout)
	}
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	for _, m := range members {
		if m.Branch != "oculus/cross-cut" {
			t.Errorf("branch = %q, want oculus/cross-cut", m.Branch)
		}
		if _, err := os.Stat(m.Path); err != nil {
			t.Errorf("member checkout missing: %v", err)
		}
		if filepath.Dir(m.Path) != layout {
			t.Errorf("member %q not under layout %q", m.Path, layout)
		}
	}
	// Base commits are the repos' HEADs.
	if members[0].BaseCommit != baseA || members[1].BaseCommit != baseB {
		t.Errorf("base commits = %q,%q want %q,%q", members[0].BaseCommit, members[1].BaseCommit, baseA, baseB)
	}

	// A duplicate name collides on the layout dir.
	if _, _, err := CreateWorkspace(base, "Cross Cut", []string{repoA, repoB}, nil); err == nil {
		t.Errorf("expected error on duplicate workspace path")
	}

	if err := RemoveWorkspace(layout, members, true); err != nil {
		t.Fatalf("RemoveWorkspace: %v", err)
	}
	if _, err := os.Stat(layout); !os.IsNotExist(err) {
		t.Errorf("layout dir survived removal: %v", err)
	}
}

func TestCreateWorkspaceRollsBackOnBadRepo(t *testing.T) {
	repoA := t.TempDir()
	gitInit(t, repoA)
	notARepo := t.TempDir() // no git init → RepoRoot fails on the 2nd member
	base := t.TempDir()

	_, _, err := CreateWorkspace(base, "ws", []string{repoA, notARepo}, nil)
	if err == nil {
		t.Fatal("expected error for non-repo member")
	}
	// The layout dir (and the first member's worktree) must be rolled back.
	if _, statErr := os.Stat(filepath.Join(base, "ws")); !os.IsNotExist(statErr) {
		t.Errorf("layout not rolled back: %v", statErr)
	}
}

func TestCreateWorkspaceNeedsTwoRepos(t *testing.T) {
	repoA := t.TempDir()
	gitInit(t, repoA)
	if _, _, err := CreateWorkspace(t.TempDir(), "solo", []string{repoA}, nil); err == nil {
		t.Error("expected error for a single-repo workspace")
	}
}
