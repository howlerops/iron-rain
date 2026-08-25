package hub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/project"
)

// cloneRoots nominates where a new checkout should land. It is derived rather than configured, so
// what matters is that it reads the user's real habits out of the projects they already have —
// and ignores the noise that made the folder list unusable in the first place.
func TestCloneRootsPrefersWhereRepositoriesActuallyLive(t *testing.T) {
	h := New()
	reg := mustRegistry(t)
	h.SetProjects(reg)

	base := t.TempDir()
	// Three repos under one parent, one under another: the first parent is where this person keeps
	// their work, and nobody had to say so.
	for _, p := range []string{"totango/alpha", "totango/beta", "totango/gamma", "side/solo"} {
		dir := filepath.Join(base, p)
		mkRepo(t, dir)
		if _, err := reg.Add(dir); err != nil {
			t.Fatalf("add %s: %v", dir, err)
		}
	}

	roots := h.cloneRoots()
	if len(roots) == 0 {
		t.Fatal("no clone roots derived from four projects")
	}
	if want := filepath.Join(base, "totango"); roots[0] != want {
		t.Errorf("first root = %q, want %q (the parent with the most repos)", roots[0], want)
	}
}

// Worktrees and temp directories are per-task artefacts. Their parents are folders nobody clones
// into, so counting them would nominate exactly the wrong destination — and these are not
// hypothetical: pr-worktrees and a /var/folders temp path both appeared in the real list.
func TestCloneRootsIgnoresWorktreesAndTempDirs(t *testing.T) {
	h := New()
	reg := mustRegistry(t)
	h.SetProjects(reg)

	base := t.TempDir()
	real := filepath.Join(base, "code", "app")
	mkRepo(t, real)
	if _, err := reg.Add(real); err != nil {
		t.Fatal(err)
	}
	// Two worktrees and a temp checkout, outnumbering the real one.
	for _, p := range []string{
		"pr-worktrees/AI-3118/app",
		"pr-worktrees/AI-3126/app",
		"tmp/T/opencode/app-update",
	} {
		dir := filepath.Join(base, p)
		mkRepo(t, dir)
		if _, err := reg.Add(dir); err != nil {
			t.Fatal(err)
		}
	}

	roots := h.cloneRoots()
	if len(roots) == 0 {
		t.Fatal("the one real repository should still yield a root")
	}
	if want := filepath.Join(base, "code"); roots[0] != want {
		t.Errorf("first root = %q, want %q — a worktree parent outvoted the real one", roots[0], want)
	}
	for _, r := range roots {
		if isWorktreeish(r) {
			t.Errorf("%q is scratch space and must not be offered as a clone destination", r)
		}
	}
}

func TestIsWorktreeish(t *testing.T) {
	scratch := []string{
		"/Users/x/totango/pr-worktrees/AI-3118/totango-agentic",
		"/Users/x/.oculus/worktrees/repo/subtask-ab12cd",
		"/private/var/folders/5q/r1rw9tjx4pl2phchr2rhc_lm0000gp/T/opencode/agentic-ai3095-update",
		"/Users/x/.oculus/worktrees/oculus/subtask-93fa72",
	}
	for _, p := range scratch {
		if !isWorktreeish(p) {
			t.Errorf("%q should be recognised as scratch space", p)
		}
	}
	keep := []string{
		"/Users/x/totango/unison-integrations",
		"/Users/x/projects/oculus",
		"/Users/x/work/worktree-manager", // names it, isn't one
	}
	for _, p := range keep {
		if isWorktreeish(p) {
			t.Errorf("%q is a real repository and must not be filtered out", p)
		}
	}
}

// No projects yet is the first-run state, not a failure. It must not panic or invent a destination.
func TestCloneRootsWithNoProjects(t *testing.T) {
	h := New()
	h.SetProjects(mustRegistry(t))
	if roots := h.cloneRoots(); len(roots) != 0 {
		t.Errorf("expected no roots from an empty registry, got %v", roots)
	}
	// And with no registry attached at all.
	if roots := New().cloneRoots(); len(roots) != 0 {
		t.Errorf("expected no roots with no registry, got %v", roots)
	}
}

// mustRegistry returns an empty project registry backed by a throwaway file.
func mustRegistry(t *testing.T) *project.Registry {
	t.Helper()
	reg, err := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("project.Load: %v", err)
	}
	return reg
}

// mkRepo creates a directory that looks like a git checkout.
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkRepo %s: %v", dir, err)
	}
}
