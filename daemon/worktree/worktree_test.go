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

// realPath resolves symlinks the way git does before answering a rev-parse. On macOS every
// t.TempDir() lives under /var/folders, which is a symlink to /private/var/folders, so comparing
// git's answer to a raw TempDir path fails on the prefix alone.
func realPath(t *testing.T, p string) string {
	t.Helper()
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return rp
}

// TestMainRepoRoot_LinkedWorktree is the regression this whole resolver exists for: inside a
// linked worktree RepoRoot answers with the worktree, which made auto-registration mint a new
// project per session branch. MainRepoRoot must answer with the repo that owns it.
func TestMainRepoRoot_LinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	wt, err := Create(t.TempDir(), repo, "feature x")
	if err != nil {
		t.Fatal(err)
	}
	want := realPath(t, repo)

	// The behaviour being corrected — asserted so this test fails loudly if RepoRoot ever
	// starts doing the rollup itself and MainRepoRoot becomes redundant.
	if got, err := RepoRoot(wt.Path); err != nil || realPath(t, got) != realPath(t, wt.Path) {
		t.Fatalf("RepoRoot(worktree) = %q (%v), want the worktree itself %q", got, err, wt.Path)
	}

	got, err := MainRepoRoot(wt.Path)
	if err != nil {
		t.Fatalf("MainRepoRoot(%s): %v", wt.Path, err)
	}
	if realPath(t, got) != want {
		t.Errorf("MainRepoRoot(worktree) = %q, want the main repo %q", got, want)
	}

	// A cwd deeper inside the worktree resolves the same: agents rarely sit at the top level.
	sub := filepath.Join(wt.Path, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := MainRepoRoot(sub); err != nil || realPath(t, got) != want {
		t.Errorf("MainRepoRoot(worktree subdir) = %q (%v), want %q", got, err, want)
	}
}

// TestMainRepoRoot_PlainCheckout pins the no-op case: an ordinary clone must resolve exactly
// where it does today, from its root and from any subdirectory.
func TestMainRepoRoot_PlainCheckout(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	want := realPath(t, repo)

	got, err := MainRepoRoot(repo)
	if err != nil {
		t.Fatalf("MainRepoRoot(%s): %v", repo, err)
	}
	if realPath(t, got) != want {
		t.Errorf("MainRepoRoot(repo) = %q, want %q", got, want)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A subdir's --git-common-dir comes back RELATIVE ("../.git"), so this exercises the
	// path-shape rejection and the RepoRoot fallback rather than the common-dir happy path.
	if got, err := MainRepoRoot(sub); err != nil || realPath(t, got) != want {
		t.Errorf("MainRepoRoot(subdir) = %q (%v), want %q", got, err, want)
	}
	if _, err := MainRepoRoot(t.TempDir()); err == nil {
		t.Error("expected MainRepoRoot to fail outside a git repo, like RepoRoot does")
	}
}

// TestMainRepoRoot_BareRepoNotWorseThanRepoRoot covers the layout that would break a naive
// "strip /.git" implementation: for a worktree of a BARE repo the common dir is the bare repo
// itself (…/x.git), whose parent is just the folder it was cloned into. Registering that would
// hand the user a project with no work tree; falling back to the worktree is merely no better
// than today, which is the bar every fallback has to clear.
func TestMainRepoRoot_BareRepoNotWorseThanRepoRoot(t *testing.T) {
	src := t.TempDir()
	gitInit(t, src)
	dir := t.TempDir()
	bare := filepath.Join(dir, "x.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, bare).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v (%s)", err, out)
	}
	wtPath := filepath.Join(dir, "bwt")
	if out, err := exec.Command("git", "-C", bare, "worktree", "add", "-q", wtPath, "-b", "bwt").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v (%s)", err, out)
	}
	got, err := MainRepoRoot(wtPath)
	if err != nil {
		t.Fatalf("MainRepoRoot(%s): %v", wtPath, err)
	}
	if realPath(t, got) != realPath(t, wtPath) {
		t.Errorf("MainRepoRoot(worktree of bare repo) = %q, want the worktree %q (never the bare dir or its parent)", got, wtPath)
	}
	if realPath(t, got) == realPath(t, dir) {
		t.Errorf("MainRepoRoot resolved to the bare repo's parent %q — that folder is not a repo", dir)
	}
}

// TestMainRepoRoot_SubmoduleStaysPut guards the most damaging wrong answer available: a
// submodule's common dir is <super>/.git/modules/<name>, and rolling it up would silently point
// the user's sessions at the superproject instead of the module they were working in.
func TestMainRepoRoot_SubmoduleStaysPut(t *testing.T) {
	super, mod := t.TempDir(), t.TempDir()
	gitInit(t, super)
	gitInit(t, mod)
	out, err := exec.Command("git", "-C", super, "-c", "protocol.file.allow=always",
		"-c", "user.email=t@t", "-c", "user.name=t", "submodule", "add", "-q", mod, "sub/mod").CombinedOutput()
	if err != nil {
		// Some git builds refuse local-file submodules outright; the assertion below is only
		// meaningful with a real one, so skip rather than fail on the environment.
		t.Skipf("git submodule add unavailable here: %v (%s)", err, out)
	}
	inner := filepath.Join(super, "sub", "mod")
	got, err := MainRepoRoot(inner)
	if err != nil {
		t.Fatalf("MainRepoRoot(%s): %v", inner, err)
	}
	if realPath(t, got) != realPath(t, inner) {
		t.Errorf("MainRepoRoot(submodule) = %q, want the submodule itself %q — never the superproject", got, inner)
	}
}
