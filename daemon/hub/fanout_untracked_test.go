package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A NEW file is the commonest thing an agent produces, and `git diff` cannot see it.
//
// diffStat's job is to say how much each fan-out variant actually did, and it was built on `git
// diff` alone — which only ever reports files git already tracks. So a variant that wrote a document,
// added a module or added a test came back as "0 files", and the comparison screen described it as
// "this agent finished without touching the tree".
//
// Observed live: two variants each wrote a 2KB NOTES.md and the comparison offered them as two
// identical do-nothings. A compare-and-merge screen that is confidently wrong about which attempt
// did anything is worse than not having one.
func TestDiffStatCountsNewlyCreatedFiles(t *testing.T) {
	wt := newTestRepo(t)

	// Exactly the shape that failed: a brand-new, uncommitted, untracked file.
	write(t, filepath.Join(wt, "NOTES.md"), "alpha\nbeta\ngamma\n")

	files, insertions, _ := diffStat(wt, "HEAD")
	if files != 1 {
		t.Fatalf("files = %d, want 1 — a created file is not 'no changes'", files)
	}
	if insertions != 3 {
		t.Fatalf("insertions = %d, want 3", insertions)
	}
}

// Tracked edits and new files are both real work and must add up, not replace each other.
func TestDiffStatCombinesTrackedEditsAndNewFiles(t *testing.T) {
	wt := newTestRepo(t)
	write(t, filepath.Join(wt, "f"), "x\nedited\n")     // tracked, modified
	write(t, filepath.Join(wt, "NEW.md"), "one\ntwo\n") // untracked, created

	files, insertions, _ := diffStat(wt, "HEAD")
	if files != 2 {
		t.Fatalf("files = %d, want 2 (1 edited + 1 created)", files)
	}
	if insertions < 3 {
		t.Fatalf("insertions = %d, want at least 3", insertions)
	}
}

// .gitignore'd output must NOT be counted as the agent's work — otherwise every build directory in
// the worktree inflates the comparison and the biggest-change-first ordering becomes meaningless.
func TestDiffStatIgnoresIgnoredFiles(t *testing.T) {
	wt := newTestRepo(t)
	write(t, filepath.Join(wt, ".gitignore"), "build/\n")
	git(t, wt, "add", ".gitignore")
	git(t, wt, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "ignore")

	if err := os.MkdirAll(filepath.Join(wt, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, "build", "artifact.bin"), strings.Repeat("x\n", 100))

	files, _, _ := diffStat(wt, "HEAD")
	if files != 0 {
		t.Fatalf("files = %d, want 0 — ignored build output is not the agent's work", files)
	}
}

// A worktree the agent never touched must still read as untouched.
func TestDiffStatReportsNothingForACleanWorktree(t *testing.T) {
	wt := newTestRepo(t)
	files, insertions, deletions := diffStat(wt, "HEAD")
	if files != 0 || insertions != 0 || deletions != 0 {
		t.Fatalf("clean worktree reported %d/%d/%d, want 0/0/0", files, insertions, deletions)
	}
}

// A file with no trailing newline still has a last line.
func TestUntrackedStatCountsALastLineWithoutNewline(t *testing.T) {
	wt := newTestRepo(t)
	write(t, filepath.Join(wt, "a.txt"), "one\ntwo")
	_, insertions := untrackedStat(context.Background(), wt)
	if insertions != 2 {
		t.Fatalf("insertions = %d, want 2", insertions)
	}
}

// Reuses the package's existing `git` and `initRepo` helpers (workspace_pr_test.go) rather than
// declaring a second pair — two ways to make a test repo in one package is how they drift.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initRepo(t, dir)
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
