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
