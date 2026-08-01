package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeIntoDefaultLandsTheWork closes the finish loop for repos with NO remote.
//
// Finishing a worktree offered exactly one destination — open a GitHub PR — so a local-only repo hit
// a dead end: the agent's work sat on a branch with no way to land it from the phone. This merges the
// branch into the default branch in the main checkout, which is what "finish" means without a forge.
func TestMergeIntoDefaultLandsTheWork(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	base := DefaultBranch(repo)

	wt, err := Create(t.TempDir(), repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("agent work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAll(context.Background(), wt.Path, "agent: add new.txt"); err != nil {
		t.Fatal(err)
	}

	if err := MergeIntoDefault(context.Background(), repo, wt.Branch); err != nil {
		t.Fatalf("MergeIntoDefault: %v", err)
	}

	// The file must now exist in the MAIN checkout, on the default branch.
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Errorf("the agent's file is not in the main checkout after merge: %v", err)
	}
	cur := strings.TrimSpace(run(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if cur != base {
		t.Errorf("main checkout left on %q, want %q — finishing must not strand the user on a branch", cur, base)
	}
}

// A merge that would conflict must FAIL CLEANLY, leaving no half-merged working tree behind for the
// user to discover later on their laptop.
func TestMergeIntoDefaultRefusesConflicts(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	// Both branches edit the same line.
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-m", "seed f")

	wt, err := Create(t.TempDir(), repo, "conflicting")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f"), []byte("from the agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAll(context.Background(), wt.Path, "agent edit"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("from the human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-m", "human edit")

	err = MergeIntoDefault(context.Background(), repo, wt.Branch)
	if err == nil {
		t.Fatal("a conflicting merge must fail rather than land a broken tree")
	}
	// No merge may be left in progress.
	if _, statErr := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); statErr == nil {
		t.Error("a half-finished merge was left behind — the user would find it on their laptop")
	}
	status := run(t, repo, "status", "--porcelain")
	if strings.Contains(status, "UU ") {
		t.Errorf("conflict markers left in the working tree:\n%s", status)
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
