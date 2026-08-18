package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gcRepo builds a tiny git repo with one commit and returns its root + HEAD.
func gcRepo(t *testing.T) (root, head string) {
	t.Helper()
	root = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "first")
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, string(out[:len(out)-1])
}

// TestRemoveIfUnchangedRemovesACleanWorktree: the common case — an agent looked around, changed
// nothing, and its worktree is pure residue. Automatic cleanup has to handle this or nothing ever
// gets tidied without a human.
func TestRemoveIfUnchangedRemovesACleanWorktree(t *testing.T) {
	root, head := gcRepo(t)
	base := t.TempDir()
	wt, err := Create(base, root, "clean")
	if err != nil {
		t.Fatal(err)
	}
	removed, why, err := RemoveIfUnchanged(root, wt.Path, head)
	if err != nil {
		t.Fatalf("unexpected error: %v (%s)", err, why)
	}
	if !removed {
		t.Fatalf("refused to remove an untouched worktree: %s", why)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatal("worktree directory is still on disk")
	}
}

// TestRemoveIfUnchangedRefusesUncommittedWork is the one that matters. Cleanup running on a timer
// must never be able to delete work someone left in a worktree — deciding by age cannot tell an
// abandoned scratch tree from an afternoon of uncommitted work, and gets it wrong in the direction
// you cannot undo.
func TestRemoveIfUnchangedRefusesUncommittedWork(t *testing.T) {
	root, head := gcRepo(t)
	base := t.TempDir()
	wt, err := Create(base, root, "dirty")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "unsaved.txt"), []byte("an afternoon of work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, why, _ := RemoveIfUnchanged(root, wt.Path, head)
	if removed {
		t.Fatal("DELETED a worktree holding uncommitted work — this is the unrecoverable failure")
	}
	if why == "" {
		t.Fatal("refused without saying why; the user cannot act on silence")
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "unsaved.txt")); err != nil {
		t.Fatalf("the uncommitted file is gone: %v", err)
	}
}

// TestRemoveIfUnchangedRefusesUnmergedCommits: committing inside a worktree makes it clean by
// `git status`, but the commits exist nowhere else. Cleanup must weigh those too, or "I committed
// it" becomes the thing that got it deleted.
func TestRemoveIfUnchangedRefusesUnmergedCommits(t *testing.T) {
	root, head := gcRepo(t)
	base := t.TempDir()
	wt, err := Create(base, root, "committed")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "b.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "work"}} {
		cmd := exec.Command("git", append([]string{"-C", wt.Path}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	removed, why, _ := RemoveIfUnchanged(root, wt.Path, head)
	if removed {
		t.Fatal("deleted a worktree carrying commits that exist nowhere else")
	}
	if why == "" {
		t.Fatal("refused without a reason")
	}
}

// TestSweepOrphansOnlyTakesTheDead: the sweep must remove worktrees whose repo is gone and leave
// every live one alone. A live worktree's .git points at an admin dir that resolves, which is the
// property the sweep keys on.
func TestSweepOrphansOnlyTakesTheDead(t *testing.T) {
	base := t.TempDir()

	// A live worktree, in a repo that still exists.
	liveRoot, _ := gcRepo(t)
	live, err := Create(base, liveRoot, "live")
	if err != nil {
		t.Fatal(err)
	}

	// An orphan: created against a repo we then delete, exactly like a test temp repo.
	deadRoot, _ := gcRepo(t)
	dead, err := Create(base, deadRoot, "dead")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(deadRoot); err != nil {
		t.Fatal(err)
	}

	n, err := SweepOrphans(base)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("swept %d worktrees, want exactly 1 (the orphan)", n)
	}
	if _, err := os.Stat(dead.Path); !os.IsNotExist(err) {
		t.Fatal("the orphan is still on disk")
	}
	if _, err := os.Stat(live.Path); err != nil {
		t.Fatalf("THE SWEEP DELETED A LIVE WORKTREE: %v", err)
	}
}

// TestAutoLinkFindsWorkspacePackages: the root node_modules alone leaves a workspace unusable —
// every package has its own gitignored node_modules, absent in a fresh worktree, so the setup step
// has to do a real install anyway. On a 20GB monorepo that is the whole cost.
func TestAutoLinkFindsWorkspacePackages(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{
		"node_modules",
		filepath.Join("packages", "api", "node_modules"),
		filepath.Join("apps", "web", "node_modules"),
		filepath.Join("node_modules", ".pnpm", "react", "node_modules"), // nested INSIDE a dep dir
		filepath.Join(".git", "objects"),
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := autoLinkDirs(root)
	want := map[string]bool{
		"node_modules":              true,
		"packages/api/node_modules": true,
		"apps/web/node_modules":     true,
	}
	for _, g := range got {
		if !want[filepath.ToSlash(g)] {
			t.Fatalf("linked %q, which should not be shared separately (got %v)", g, got)
		}
		delete(want, filepath.ToSlash(g))
	}
	if len(want) != 0 {
		t.Fatalf("missed workspace dep dirs: %v (got %v)", want, got)
	}
	// The root must come first, so a parent link is created before any child is considered.
	if len(got) == 0 || filepath.ToSlash(got[0]) != "node_modules" {
		t.Fatalf("root node_modules must sort first, got %v", got)
	}
}
