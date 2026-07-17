package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"symbolic-ref", "HEAD", "refs/heads/main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out))
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Feature X":     "feature-x",
		"fix/the bug!!": "fix-the-bug",
		"  Trim Me  ":   "trim-me",
		"a__b--c":       "a-b-c",
		"":              "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateRemove(t *testing.T) {
	repo := t.TempDir()
	headHere := gitInit(t, repo)
	base := t.TempDir()

	wt, err := Create(base, repo, "Feature X")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "oculus/feature-x" {
		t.Errorf("branch = %q, want oculus/feature-x", wt.Branch)
	}
	// The worktree path exists, is inside the base, and IS a git work tree.
	if !strings.HasPrefix(wt.Path, base) {
		t.Errorf("worktree path %q not under base %q", wt.Path, base)
	}
	if out, err := exec.Command("git", "-C", wt.Path, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("worktree is not a git work tree: %v %s", err, out)
	}
	// It's checked out on the new branch, at the same commit as the repo HEAD.
	branch, _ := exec.Command("git", "-C", wt.Path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if strings.TrimSpace(string(branch)) != "oculus/feature-x" {
		t.Errorf("worktree branch = %q", strings.TrimSpace(string(branch)))
	}
	head, _ := exec.Command("git", "-C", wt.Path, "rev-parse", "HEAD").Output()
	if strings.TrimSpace(string(head)) != headHere {
		t.Errorf("worktree HEAD = %q, want %q", strings.TrimSpace(string(head)), headHere)
	}

	// Duplicate name -> error (path/branch already exist).
	if _, err := Create(base, repo, "Feature X"); err == nil {
		t.Error("expected error creating a duplicate worktree")
	}

	// Remove cleans it up.
	if err := Remove(repo, wt.Path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists after remove: %v", err)
	}
}

func TestRemoveDirtyNeedsForce(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	wt, err := Create(t.TempDir(), repo, "dirty")
	if err != nil {
		t.Fatal(err)
	}
	// Make the worktree dirty.
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(repo, wt.Path, false); err == nil {
		t.Error("expected non-force remove of a dirty worktree to fail")
	}
	if err := Remove(repo, wt.Path, true); err != nil {
		t.Fatalf("force remove failed: %v", err)
	}
}

func TestRepoRoot_NonRepo(t *testing.T) {
	if _, err := RepoRoot(t.TempDir()); err == nil {
		t.Error("expected RepoRoot to fail outside a git repo")
	}
}
