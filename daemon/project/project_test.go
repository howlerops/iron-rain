package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "init.defaultBranch=main", "symbolic-ref", "HEAD", "refs/heads/main"},
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
}

func TestRegistry_AddGitRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)

	r, err := Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Add(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsGitRepo {
		t.Error("expected IsGitRepo=true")
	}
	if p.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", p.DefaultBranch)
	}
	if p.Name != filepath.Base(repo) {
		t.Errorf("Name = %q, want %q", p.Name, filepath.Base(repo))
	}
	if p.ID == "" || p.Path == "" {
		t.Error("ID/Path must be set")
	}
}

func TestRegistry_AddPlainDir_And_Dedup(t *testing.T) {
	dir := t.TempDir()
	r, _ := Load(filepath.Join(t.TempDir(), "p.json"))

	p1, err := r.Add(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p1.IsGitRepo {
		t.Error("plain dir must not be a git repo")
	}
	// Adding the same path again is idempotent (same ID, no duplicate).
	p2, err := r.Add(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID != p1.ID {
		t.Errorf("dedup failed: %q != %q", p2.ID, p1.ID)
	}
	if n := len(r.List()); n != 1 {
		t.Errorf("List len = %d, want 1", n)
	}
}

func TestRegistry_AddMissingPath(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "p.json"))
	if _, err := r.Add(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error adding a non-existent path")
	}
	// A file (not a dir) is rejected too.
	f := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if _, err := r.Add(f); err == nil {
		t.Fatal("expected error adding a file")
	}
}

func TestRegistry_RemoveAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	dirA, dirB := t.TempDir(), t.TempDir()

	r, _ := Load(path)
	a, _ := r.Add(dirA)
	_, _ = r.Add(dirB)
	if err := r.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(a.ID); ok {
		t.Error("removed project still present")
	}

	// Reload from disk: only dirB survives, persisted.
	r2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r2.List()
	if len(got) != 1 || got[0].Path != dirB {
		t.Fatalf("persisted list = %+v, want only %s", got, dirB)
	}
}
