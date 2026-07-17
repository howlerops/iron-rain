package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

// TestGitInfoRespectsContext verifies gitInfo honors its context: an already-cancelled
// context aborts the git subprocesses instead of hanging (regression for the ctx-less exec).
func TestGitInfoRespectsContext(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	isRepo, branch := gitInfo(ctx, repo)
	if isRepo || branch != "" {
		t.Fatalf("cancelled ctx should abort git: isRepo=%v branch=%q", isRepo, branch)
	}
}

// TestRegistry_ConcurrentAdd exercises many parallel Adds. It guards against lost updates
// now that the git detection and disk write happen off the registry lock: every project
// must survive both in memory and on disk (run with -race to catch data races).
func TestRegistry_ConcurrentAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	r, _ := Load(path)

	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			if _, err := r.Add(d); err != nil {
				t.Errorf("Add(%s): %v", d, err)
			}
		}(d)
	}
	wg.Wait()

	if n := len(r.List()); n != len(dirs) {
		t.Fatalf("in-memory list = %d, want %d", n, len(dirs))
	}
	r2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r2.List()); n != len(dirs) {
		t.Fatalf("persisted list = %d, want %d (a concurrent save lost an update)", n, len(dirs))
	}
}

func TestRegistry_Source(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	auto, manual := t.TempDir(), t.TempDir()

	r, _ := Load(path)
	a, _ := r.AddAuto(auto)
	if a.Source != "auto" {
		t.Fatalf("AddAuto source = %q, want auto", a.Source)
	}
	m, _ := r.Add(manual)
	if m.Source != "manual" {
		t.Fatalf("Add source = %q, want manual", m.Source)
	}

	// Promotion: manually adding a previously auto-discovered path keeps the same
	// project but flips it to "manual" (the user's explicit keep wins).
	p, _ := r.Add(auto)
	if p.ID != a.ID {
		t.Fatalf("promote created a new project: %s vs %s", p.ID, a.ID)
	}
	if p.Source != "manual" {
		t.Fatalf("promoted source = %q, want manual", p.Source)
	}

	// No downgrade: auto-discovering a manual path leaves it manual.
	if p2, _ := r.AddAuto(manual); p2.Source != "manual" {
		t.Fatalf("auto re-add downgraded to %q, want manual", p2.Source)
	}

	// Source survives a reload from disk.
	r2, _ := Load(path)
	src := map[string]string{}
	for _, pp := range r2.List() {
		src[pp.ID] = pp.Source
	}
	if len(r2.List()) != 2 {
		t.Fatalf("want 2 projects after reload, got %d", len(r2.List()))
	}
	if src[a.ID] != "manual" {
		t.Fatalf("reloaded promoted source = %q, want manual", src[a.ID])
	}
}
