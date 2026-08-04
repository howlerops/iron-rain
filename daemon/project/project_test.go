package project

import (
	"context"
	"encoding/json"
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

// realPath resolves symlinks the way git does before answering a rev-parse: on macOS a
// t.TempDir() sits under /var/folders, a symlink to /private/var/folders, so the repo root the
// migration records never matches a raw TempDir string.
func realPath(t *testing.T, p string) string {
	t.Helper()
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return rp
}

// addWorktree creates a linked worktree of repo and returns its path.
func addWorktree(t *testing.T, repo, path, branch string) string {
	t.Helper()
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", path, "-b", branch).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add %s: %v (%s)", path, err, out)
	}
	return path
}

// writeRegistry lays down a projects.json exactly as an older daemon would have left it.
func writeRegistry(t *testing.T, path string, list []Project) {
	t.Helper()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func autoEntry(path string) Project {
	return Project{ID: "proj_" + shortHash(path), Name: filepath.Base(path), Path: path, IsGitRepo: true, Source: "auto"}
}

// TestLoad_CollapsesAutoWorktrees is the migration's reason to exist: users upgrading already
// have one auto project per worktree session, and changing the resolver alone would leave that
// clutter in place forever because nothing else ever prunes it.
func TestLoad_CollapsesAutoWorktrees(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	base := t.TempDir()
	wt1 := addWorktree(t, repo, filepath.Join(base, "one"), "oculus/one")
	wt2 := addWorktree(t, repo, filepath.Join(base, "two"), "oculus/two")

	path := filepath.Join(t.TempDir(), "projects.json")
	old1, old2 := autoEntry(wt1), autoEntry(wt2)
	writeRegistry(t, path, []Project{old1, old2})

	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("want the 2 worktrees collapsed into 1 repo, got %d: %+v", len(got), got)
	}
	if realPath(t, got[0].Path) != realPath(t, repo) {
		t.Errorf("collapsed project path = %q, want the repo %q", got[0].Path, repo)
	}
	if got[0].Source != "auto" {
		t.Errorf("collapsed project source = %q, want auto", got[0].Source)
	}
	if got[0].DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want the repo's main (never a worktree's oculus/* branch)", got[0].DefaultBranch)
	}

	// Both dead IDs still resolve, so a loop or approval rule pinned to a worktree keeps
	// running instead of failing with "unknown project" the first time it fires after upgrade.
	for _, old := range []Project{old1, old2} {
		p, ok := r.Get(old.ID)
		if !ok {
			t.Fatalf("collapsed worktree id %s no longer resolves", old.ID)
		}
		if p.ID != got[0].ID {
			t.Errorf("id %s resolved to %s, want the repo %s", old.ID, p.ID, got[0].ID)
		}
	}

	// The collapse is persisted, and reloading a migrated registry is a no-op (no churn, no
	// second round of git subprocesses rewriting the file at every daemon start).
	r2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got2 := r2.List(); len(got2) != 1 || got2[0].ID != got[0].ID {
		t.Fatalf("reload changed the registry: %+v", got2)
	}
}

// TestLoad_KeepsManualWorktreeEntries: adding a worktree through the Projects UI is a stated
// intent to track it separately. A migration that deleted it would destroy user configuration.
func TestLoad_KeepsManualWorktreeEntries(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	base := t.TempDir()
	kept := addWorktree(t, repo, filepath.Join(base, "keep"), "oculus/keep")
	folded := addWorktree(t, repo, filepath.Join(base, "fold"), "oculus/fold")

	manual := autoEntry(kept)
	manual.Source = "manual"
	path := filepath.Join(t.TempDir(), "projects.json")
	writeRegistry(t, path, []Project{manual, autoEntry(folded)})

	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("want the manual worktree + the repo, got %d: %+v", len(got), got)
	}
	var sawManual bool
	for _, p := range got {
		if p.Path == kept {
			sawManual = true
			if p.Source != "manual" || p.ID != manual.ID || p.Name != manual.Name {
				t.Errorf("manual worktree entry was rewritten: %+v", p)
			}
		}
	}
	if !sawManual {
		t.Error("manual worktree entry was collapsed away")
	}
}

// TestLoad_ManualRepoAbsorbsWorktreeIDs: when the repo is already registered — however it got
// there — its worktrees fold into that entry instead of conjuring a duplicate beside it.
func TestLoad_ManualRepoAbsorbsWorktreeIDs(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	wt := addWorktree(t, repo, filepath.Join(t.TempDir(), "one"), "oculus/one")

	repoEntry := autoEntry(realPath(t, repo))
	repoEntry.Source = "manual"
	old := autoEntry(wt)
	path := filepath.Join(t.TempDir(), "projects.json")
	// Worktree first: the repo's own entry appears LATER in the list, the ordering that a
	// single-pass migration would duplicate.
	writeRegistry(t, path, []Project{old, repoEntry})

	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("want 1 repo entry, got %d: %+v", len(got), got)
	}
	if got[0].ID != repoEntry.ID || got[0].Source != "manual" {
		t.Errorf("the existing repo entry was replaced instead of reused: %+v", got[0])
	}
	if p, ok := r.Get(old.ID); !ok || p.ID != repoEntry.ID {
		t.Errorf("worktree id %s did not resolve to the existing repo entry", old.ID)
	}
}

// TestLoad_MissingWorktreePathSurvives: a worktree directory the user deleted by hand must not
// break the load, and must not be silently dropped either — a failed probe is not evidence that
// an entry is redundant.
func TestLoad_MissingWorktreePathSurvives(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	gone := addWorktree(t, repo, filepath.Join(t.TempDir(), "gone"), "oculus/gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir() // a non-git folder, another shape the resolver can't roll up

	path := filepath.Join(t.TempDir(), "projects.json")
	writeRegistry(t, path, []Project{autoEntry(gone), autoEntry(plain)})

	r, err := Load(path)
	if err != nil {
		t.Fatalf("a vanished worktree must not fail the load: %v", err)
	}
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("unresolvable entries must be kept, got %d: %+v", len(got), got)
	}
}

// TestLoad_PlainRepoEntriesUntouched: an auto entry that is already a repo root is the common
// case after migration, and must survive byte-for-byte.
func TestLoad_PlainRepoEntriesUntouched(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	entry := autoEntry(realPath(t, repo))
	entry.DefaultBranch = "main"

	path := filepath.Join(t.TempDir(), "projects.json")
	writeRegistry(t, path, []Project{entry})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("plain repo entry changed: %+v, want just %+v", got, entry)
	}
	if got[0].ID != entry.ID || got[0].Path != entry.Path || got[0].Name != entry.Name ||
		got[0].Source != entry.Source || got[0].DefaultBranch != entry.DefaultBranch ||
		len(got[0].AbsorbedIDs) != 0 {
		t.Fatalf("plain repo entry changed: %+v, want %+v", got[0], entry)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a registry with nothing to collapse must not be rewritten on load")
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
