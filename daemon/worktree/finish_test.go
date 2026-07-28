package worktree

import (
	"context"
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
	diff, err := Diff(context.Background(), wt.Path, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "changed") || !strings.Contains(diff, "diff --git") {
		t.Fatalf("diff missing the change:\n%s", diff)
	}
}

// TestWouldConflict proves the non-destructive conflict check: it flags a worktree branch that
// would conflict with main (both edited the same lines) WITHOUT leaving a mid-merge state, and
// reports no conflict when the branches touch different files.
func TestWouldConflict(t *testing.T) {
	commit := func(dir, msg string) {
		t.Helper()
		for _, args := range [][]string{{"add", "."}, {"commit", "-qm", msg}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v (%s)", args, err, out)
			}
		}
	}

	// --- conflicting case: worktree branch and main edit the SAME file's content ---
	repo := t.TempDir()
	gitInit(t, repo) // main: f="x"
	wt, err := Create(t.TempDir(), repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f"), []byte("feature change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(wt.Path, "feature edit")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("main change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(repo, "main edit")

	paths, err := WouldConflict(context.Background(), wt.Path, "main")
	if err != nil {
		t.Fatalf("WouldConflict: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected a conflict on 'f', got none")
	}
	found := false
	for _, p := range paths {
		if p == "f" {
			found = true
		}
	}
	if !found {
		t.Errorf("conflicted paths = %v, want to include 'f'", paths)
	}
	// Non-destructive: the working tree must NOT be left mid-merge.
	if _, err := os.Stat(filepath.Join(wt.Path, ".git")); err != nil {
		// worktrees use a .git file, not dir — just assert no MERGE_HEAD in the repo's git dir.
	}
	if out, _ := exec.Command("git", "-C", wt.Path, "status", "--porcelain=v2").CombinedOutput(); strings.Contains(string(out), "MERGE") {
		t.Errorf("worktree left mid-merge: %s", out)
	}

	// --- non-conflicting case: different files ---
	repo2 := t.TempDir()
	gitInit(t, repo2)
	wt2, err := Create(t.TempDir(), repo2, "feature2")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt2.Path, "newfile"), []byte("only here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(wt2.Path, "add newfile")
	if err := os.WriteFile(filepath.Join(repo2, "other"), []byte("main only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(repo2, "add other")
	paths2, err := WouldConflict(context.Background(), wt2.Path, "main")
	if err != nil {
		t.Fatalf("WouldConflict (clean): %v", err)
	}
	if len(paths2) != 0 {
		t.Errorf("expected no conflict, got %v", paths2)
	}
}

// TestSnapshotAndRestore proves checkpoints: snapshot the worktree at one point, keep working, then
// restore tracked files back to the checkpoint — turning the durable timeline into a rollback UI.
func TestSnapshotAndRestore(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo) // f="x"
	wt, err := Create(t.TempDir(), repo, "work")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Turn 1: edit f to v1, add a new tracked file, then CHECKPOINT.
	if err := os.WriteFile(filepath.Join(wt.Path, "f"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "g"), []byte("g1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp, err := Snapshot(ctx, wt.Path)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cp == "" {
		t.Fatal("empty snapshot sha")
	}

	// Turn 2: make it worse — clobber f and g.
	if err := os.WriteFile(filepath.Join(wt.Path, "f"), []byte("BROKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "g"), []byte("BROKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Roll back to the checkpoint.
	if err := RestoreSnapshot(ctx, wt.Path, cp); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	for name, want := range map[string]string{"f": "v1\n", "g": "g1\n"} {
		got, err := os.ReadFile(filepath.Join(wt.Path, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("after restore, %s = %q, want %q", name, got, want)
		}
	}
}

// TestSnapshotCleanTreeReturnsHead: a checkpoint on a clean tree resolves to HEAD (always restorable).
func TestSnapshotCleanTreeReturnsHead(t *testing.T) {
	repo := t.TempDir()
	head := gitInit(t, repo)
	sha, err := Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if sha != head {
		t.Errorf("clean-tree snapshot = %s, want HEAD %s", sha, head)
	}
}

// TestDiff_ExcludesStderrWarnings verifies Diff returns only stdout, so git's
// stderr warnings (e.g. CRLF conversion notices) never corrupt the diff text.
func TestDiff_ExcludesStderrWarnings(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	base, err := HeadCommit(repo)
	if err != nil {
		t.Fatal(err)
	}
	// autocrlf=true makes git emit "warning: ... CRLF will be replaced by LF" on
	// files with CRLF line endings.
	if out, err := exec.Command("git", "-C", repo, "config", "core.autocrlf", "true").CombinedOutput(); err != nil {
		t.Fatalf("config autocrlf: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "crlf.txt"), []byte("a\r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", "crlf.txt").CombinedOutput(); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}

	diff, err := Diff(context.Background(), repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "crlf.txt") {
		t.Errorf("Diff missing the actual change:\n%s", diff)
	}
	// Deterministic: Diff must equal git's stdout only, never stdout+stderr.
	stdoutOnly, err := exec.Command("git", "-C", repo, "diff", base).Output()
	if err != nil {
		t.Fatal(err)
	}
	if diff != string(stdoutOnly) {
		t.Errorf("Diff != git stdout; stderr may be leaking in.\ngot:  %q\nwant: %q", diff, string(stdoutOnly))
	}

	// Stronger check when git actually emits a stderr warning in this environment.
	combined, _ := exec.Command("git", "-C", repo, "diff", base).CombinedOutput()
	if strings.Contains(string(combined), "warning:") && strings.Contains(diff, "warning:") {
		t.Errorf("Diff leaked a stderr warning into the diff text:\n%s", diff)
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
	if committed, err := CommitAll(context.Background(), wt.Path, "empty"); err != nil || committed {
		t.Fatalf("CommitAll on clean tree = %v,%v want false,nil", committed, err)
	}

	// Make a change, commit it, push the branch.
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := CommitAll(context.Background(), wt.Path, "add new.txt")
	if err != nil || !committed {
		t.Fatalf("CommitAll = %v,%v want true,nil", committed, err)
	}
	if err := Push(context.Background(), wt.Path, wt.Branch); err != nil {
		t.Fatal(err)
	}
	// The remote now has the branch.
	out, err := exec.Command("git", "-C", remote, "rev-parse", "--verify", wt.Branch).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("pushed branch not on remote: %v", err)
	}
}

func TestChangedFilesAndOverlaps(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	base, _ := HeadCommit(repo)
	wtA, _ := Create(t.TempDir(), repo, "a")
	wtB, _ := Create(t.TempDir(), repo, "b")

	// Both worktrees edit the shared file "f"; B also adds "b-only".
	_ = os.WriteFile(filepath.Join(wtA.Path, "f"), []byte("A"), 0o644)
	_ = os.WriteFile(filepath.Join(wtB.Path, "f"), []byte("B"), 0o644)
	_ = os.WriteFile(filepath.Join(wtB.Path, "b-only"), []byte("x"), 0o644)

	ca, err := ChangedFiles(wtA.Path, base)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(ca, "f") {
		t.Fatalf("A changed files = %v, want to include f", ca)
	}
	cb, _ := ChangedFiles(wtB.Path, base)

	changed := map[string][]string{"a": ca, "b": cb}
	ov := Overlaps("a", changed)
	if len(ov["f"]) != 1 || ov["f"][0] != "b" {
		t.Fatalf("Overlaps(a)[f] = %v, want [b]", ov["f"])
	}
	if _, isConflict := ov["b-only"]; isConflict {
		t.Error("b-only is not shared with a; should not be flagged")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestContextCancelsGitExec proves the ctx threaded into Diff/CommitAll actually
// governs the git subprocess: against a valid repo (where they'd normally succeed), an
// already-cancelled context makes them fail fast — so the daemon can abort a hung git
// when the client/session goes away instead of only relying on the internal timeout.
func TestContextCancelsGitExec(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Diff(ctx, repo, ""); err == nil {
		t.Error("Diff with a cancelled context should error")
	}
	if _, err := CommitAll(ctx, repo, "x"); err == nil {
		t.Error("CommitAll with a cancelled context should error")
	}
}
