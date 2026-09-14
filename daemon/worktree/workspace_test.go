package worktree

import (
	"os"
	"os/exec"
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

// A refused removal must leave the work on disk.
//
// Members live at layout/<name>, and RemoveWorkspace used to os.RemoveAll(layout) unconditionally —
// so the non-force path destroyed exactly what it had just declined to destroy. `git worktree
// remove` refuses a worktree with uncommitted or untracked files; the refusal was recorded and
// returned, and the files were deleted anyway. The user saw the error and the session still listed,
// while the changes were already unrecoverable.
//
// Reachable from worktree.remove {force:false} (capSteer) and from fanout.resolve's teardown.
func TestARefusedWorkspaceRemovalKeepsTheWork(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	layout := t.TempDir()
	member := filepath.Join(layout, "app")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "feature", member).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v (%s)", err, out)
	}
	// Uncommitted work: exactly what the non-force path exists to protect.
	precious := filepath.Join(member, "notes.md")
	if err := os.WriteFile(precious, []byte("hours of uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	members := []Member{{Name: "app", RepoRoot: repo, Path: member, Branch: "feature"}}
	err := RemoveWorkspace(layout, members, false)
	if err == nil {
		t.Fatal("precondition: git should have refused a dirty worktree without --force")
	}

	if _, statErr := os.Stat(precious); statErr != nil {
		t.Fatalf("the uncommitted file was deleted despite the refusal (%v).\n\n"+
			"RemoveWorkspace reported %q and destroyed the work anyway — the user sees the error, "+
			"the session stays listed, and the changes are gone.", statErr, err)
	}
}

// The success path must still clean up completely, or every removed workspace leaks its directory.
func TestACleanWorkspaceRemovalDeletesTheLayout(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	layout := t.TempDir()
	member := filepath.Join(layout, "app")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "feature2", member).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v (%s)", err, out)
	}
	members := []Member{{Name: "app", RepoRoot: repo, Path: member, Branch: "feature2"}}
	if err := RemoveWorkspace(layout, members, false); err != nil {
		t.Fatalf("a clean workspace was refused: %v", err)
	}
	if _, err := os.Stat(layout); err == nil {
		t.Fatal("the layout directory survived a fully successful removal")
	}
}
