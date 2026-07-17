package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffAndHeadCommit(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	base, err := HeadCommit(repo)
	if err != nil || base == "" {
		t.Fatalf("HeadCommit: %v", err)
	}
	wt, err := Create(t.TempDir(), repo, "diffme")
	if err != nil {
		t.Fatal(err)
	}
	// Change a file in the worktree; the diff vs base must show it.
	if err := os.WriteFile(filepath.Join(wt.Path, "f"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := Diff(wt.Path, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "changed") || !strings.Contains(diff, "diff --git") {
		t.Fatalf("diff missing the change:\n%s", diff)
	}
}

func TestCommitAllAndPush(t *testing.T) {
	// A bare "remote" and a repo whose origin points at it.
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-q", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	repo := t.TempDir()
	gitInit(t, repo)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", remote).CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v %s", err, out)
	}
	if !HasRemote(repo) {
		t.Fatal("HasRemote = false, want true")
	}

	wt, err := Create(t.TempDir(), repo, "pushme")
	if err != nil {
		t.Fatal(err)
	}

	// Clean worktree -> CommitAll is a no-op.
	if committed, err := CommitAll(wt.Path, "empty"); err != nil || committed {
		t.Fatalf("CommitAll on clean tree = %v,%v want false,nil", committed, err)
	}

	// Make a change, commit it, push the branch.
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := CommitAll(wt.Path, "add new.txt")
	if err != nil || !committed {
		t.Fatalf("CommitAll = %v,%v want true,nil", committed, err)
	}
	if err := Push(wt.Path, wt.Branch); err != nil {
		t.Fatal(err)
	}
	// The remote now has the branch.
	out, err := exec.Command("git", "-C", remote, "rev-parse", "--verify", wt.Branch).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("pushed branch not on remote: %v", err)
	}
}
