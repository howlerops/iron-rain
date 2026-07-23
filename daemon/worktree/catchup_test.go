package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

// setupCatchUp builds: a bare origin, a local clone on branch `feature` (with commit A on file a.txt),
// and origin/main advanced by commit B. Returns the local repo path (checked out on feature). If
// `conflict`, B edits the SAME file feature does, so the merge collides.
func setupCatchUp(t *testing.T, conflict bool) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir) // main + one commit (file "f")
	origin := t.TempDir()
	git(t, origin, "init", "--bare", "-q")
	git(t, dir, "remote", "add", "origin", origin)
	git(t, dir, "push", "-q", "-u", "origin", "main")

	// Branch off main, commit A on feature.
	git(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "feature change\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "A on feature")

	// Advance origin/main with commit B (same file if conflict, else a different file).
	git(t, dir, "checkout", "-q", "main")
	if conflict {
		writeFile(t, dir, "a.txt", "main change\n")
	} else {
		writeFile(t, dir, "b.txt", "main only\n")
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "B on main")
	git(t, dir, "push", "-q", "origin", "main")

	// Back to feature (behind origin/main now).
	git(t, dir, "checkout", "-q", "feature")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCatchUpToMainCleanMerge(t *testing.T) {
	dir := setupCatchUp(t, false)
	res, err := CatchUpToMain(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "updated" {
		t.Fatalf("status = %q (%s), want updated", res.Status, res.Message)
	}
	// origin/main's file is now present on feature.
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Errorf("b.txt not merged in: %v", err)
	}
	// A second run is a no-op.
	res2, err := CatchUpToMain(context.Background(), dir)
	if err != nil || res2.Status != "up_to_date" {
		t.Fatalf("second run: status=%q err=%v, want up_to_date", res2.Status, err)
	}
}

func TestCatchUpToMainConflict(t *testing.T) {
	dir := setupCatchUp(t, true)
	res, err := CatchUpToMain(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "conflicts" {
		t.Fatalf("status = %q (%s), want conflicts", res.Status, res.Message)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "a.txt" {
		t.Fatalf("conflicts = %v, want [a.txt]", res.Conflicts)
	}
}
