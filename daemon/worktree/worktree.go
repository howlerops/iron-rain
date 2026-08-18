// Package worktree automates git worktrees so each session can run on its own isolated
// branch (the pattern every worktree ADE uses). The daemon owns the lifecycle: create a
// worktree on a fresh branch, remove it, and prune stale admin records. Worktrees live
// OUTSIDE the repo (default ~/.oculus/worktrees/<repo>/<name>) to avoid nested-git and
// repo pollution.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitOpTimeout bounds a single git invocation in the create path so a hung git — a stuck lock, a
// credential prompt with no tty, an enormous checkout — surfaces as a clear error instead of an
// infinite "starting session" spinner. Overridable for tests.
var GitOpTimeout = 90 * time.Second

func gitContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), GitOpTimeout)
}

// Worktree is a created worktree: its checkout path and the branch it's on.
type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// RepoRoot returns the git top-level of dir, or an error if dir isn't in a repo.
func RepoRoot(dir string) (string, error) {
	ctx, cancel := gitContext()
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s: git rev-parse timed out after %s (repo locked or unresponsive)", dir, GitOpTimeout)
		}
		// Surface git's real stderr (missing git, permissions, corrupt repo) rather
		// than flattening every failure to "not a git repository".
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: not a git repository: %w: %s", dir, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: not a git repository: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MainRepoRoot resolves dir to the root of its MAIN repository — the checkout that owns the
// shared .git admin directory — where RepoRoot stops at whichever work tree dir happens to sit in.
// The two differ only inside a LINKED worktree, and that difference is the whole point: there
// `git rev-parse --show-toplevel` answers with the worktree's own throwaway path (typically
// ~/.oculus/worktrees/<repo>/<session>), so auto-registering a cwd created one brand-new "project"
// per worktree session and buried the actual repo under a pile of near-identical siblings.
// `--git-common-dir` instead reports the .git a worktree BORROWS — the main checkout's — so its
// parent directory is the one repo every worktree of it belongs under.
//
// Every rung of the fallback chain lands back on RepoRoot rather than inventing a path, because a
// confidently wrong root is worse than today's over-specific one: it would register a directory
// the user never works in, and sessions spawned from it would run in the wrong tree. The shapes
// below are deliberately rejected (each fails the "<root>/.git" check and falls through):
//   - a bare repo, or a worktree of one: the common dir IS the bare repo (…/repo.git), whose
//     parent is merely the folder it was cloned into and which has no work tree to run a session in
//   - a submodule: the common dir is …/super/.git/modules/<name>, and folding a submodule into its
//     superproject would silently retarget the user's sessions at a different repository
//   - a --separate-git-dir / GIT_DIR checkout: the admin dir lives nowhere near the work tree, so
//     its parent has no relationship to the repo at all
func MainRepoRoot(dir string) (string, error) {
	if root, ok := commonDirRoot(dir); ok {
		return root, nil
	}
	return RepoRoot(dir)
}

// commonDirRoot asks git for the shared git dir and derives the main work tree root from it. It
// reports ok=false for every answer that isn't a plain "<root>/.git" present on disk, leaving the
// caller to fall back; see MainRepoRoot for which real-world repo layouts that rejects and why.
func commonDirRoot(dir string) (string, bool) {
	// One context covers both attempts so a wedged git can't spend the timeout twice over.
	ctx, cancel := gitContext()
	defer cancel()
	// --path-format=absolute needs git 2.31 (2021). Older git rejects the flag outright, so retry
	// the bare form: inside a linked worktree — the only case whose answer we act on — git reports
	// the common dir as an absolute path anyway, and a relative answer (".git", "../.git") means
	// we're already in the main checkout, where RepoRoot is by definition the right answer.
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		out, err = exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-common-dir").Output()
		if err != nil {
			return "", false
		}
	}
	common := filepath.Clean(strings.TrimSpace(string(out)))
	if !filepath.IsAbs(common) || filepath.Base(common) != ".git" {
		return "", false
	}
	// Insist the admin dir is really there. A worktree's gitfile can outlive the main checkout it
	// points at (someone deleted or moved the repo without pruning), and registering a project at
	// a path that no longer exists would put a dead entry in every user's sidebar.
	if fi, err := os.Stat(common); err != nil || !fi.IsDir() {
		return "", false
	}
	root := filepath.Dir(common)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return "", false
	}
	return root, true
}

// DefaultBase is where worktrees are created (~/.oculus/worktrees, or ./oculus-worktrees
// if the home dir is unavailable).
//
// OCULUS_WORKTREE_BASE overrides it, and exists so TESTS cannot write into the developer's real
// home. Any test that creates a worktree without passing an explicit base used to land in
// ~/.oculus/worktrees and stay there: the repo it belonged to was a t.TempDir() that Go cleaned up,
// but the worktree outside that dir was nobody's job to remove. One machine had accumulated 1133 of
// them — 967 fanout variants and 166 checkpoints — every one pointing at a long-deleted temp repo.
// An env override contains the whole class, including tests that forget to pass a base.
func DefaultBase() string {
	if base := os.Getenv("OCULUS_WORKTREE_BASE"); base != "" {
		return base
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-worktrees"
	}
	return filepath.Join(home, ".oculus", "worktrees")
}

// Create adds a worktree for the repo containing repoDir, on a new branch oculus/<name>,
// under base/<repo>/<name>. It fails if that path or branch already exists (callers pick
// unique names). An empty base uses DefaultBase().
func Create(base, repoDir, name string) (Worktree, error) {
	root, err := RepoRoot(repoDir)
	if err != nil {
		return Worktree{}, err
	}
	slug := Slug(name)
	if slug == "" {
		return Worktree{}, fmt.Errorf("worktree: empty name")
	}
	if base == "" {
		base = DefaultBase()
	}
	path := filepath.Join(base, filepath.Base(root), slug)
	return createAt(root, path, "oculus/"+slug)
}

// CreateFrom is Create with an explicit base ref to branch from ("" = the repo's current HEAD).
//
// Branching from HEAD is the historical default and the surprising one: the worktree inherits
// whatever is checked out at that moment, so a fanout's N variants all start from the same
// half-finished local state. Pinning a ref (origin/main, or one resolved commit shared by every
// variant) is what makes their results comparable to the trunk rather than only to each other.
func CreateFrom(base, repoDir, name, baseRef string) (Worktree, error) {
	if baseRef == "" {
		return Create(base, repoDir, name)
	}
	root, err := RepoRoot(repoDir)
	if err != nil {
		return Worktree{}, err
	}
	slug := Slug(name)
	if slug == "" {
		return Worktree{}, fmt.Errorf("worktree: empty name")
	}
	if base == "" {
		base = DefaultBase()
	}
	path := filepath.Join(base, filepath.Base(root), slug)
	return createAtFrom(root, path, "oculus/"+slug, baseRef)
}

// createAt adds a worktree for an already-resolved repo root at an explicit path on branch.
// Shared by Create (single repo) and CreateWorkspace (one call per member repo).
func createAt(root, path, branch string) (Worktree, error) {
	return createAtFrom(root, path, branch, "")
}

// createAtFrom is createAt with an optional base ref ("" = current HEAD).
func createAtFrom(root, path, branch, baseRef string) (Worktree, error) {
	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("worktree path already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}
	// git worktree add <path> -b <branch> [<base>] — with no base, the branch starts at HEAD.
	args := []string{"-C", root, "worktree", "add", path, "-b", branch}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	ctx, cancel := gitContext()
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Worktree{}, fmt.Errorf("git worktree add timed out after %s for %s (branch %s) — repo may be locked or the checkout too large", GitOpTimeout, root, branch)
		}
		return Worktree{}, fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return Worktree{Path: path, Branch: branch}, nil
}

// Remove deletes a worktree. A dirty (uncommitted) worktree needs force=true.
func Remove(repoDir, path string, force bool) error {
	root, err := RepoRoot(repoDir)
	if err != nil {
		// Fall back to removing the directory + pruning if the repo is already gone.
		root = repoDir
	}
	args := []string{"-C", root, "worktree", "remove", path}
	if force {
		args = append(args, "--force")
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveIfUnchanged deletes a worktree ONLY when it holds nothing a human would miss: no
// uncommitted changes, and no commits that aren't already on baseCommit. It reports whether it
// removed anything, and why not when it didn't.
//
// This is the policy Claude Code uses for agent-isolated worktrees ("auto-cleaned if unchanged"),
// and it is what makes automatic cleanup safe enough to run on a timer. The alternative — deciding
// by age — cannot tell an abandoned scratch worktree from the one holding an afternoon of
// uncommitted work, and gets it wrong in the direction you can't undo.
//
// Deliberately NOT force: `git worktree remove` refuses a dirty tree on its own, so git's check is
// the backstop even if the explicit one below is ever wrong.
func RemoveIfUnchanged(repoDir, path, baseCommit string) (removed bool, why string, err error) {
	if dirty, derr := IsDirty(path); derr != nil {
		return false, "could not inspect it: " + derr.Error(), derr
	} else if dirty {
		return false, "it has uncommitted changes", nil
	}
	if baseCommit != "" {
		out, cerr := exec.Command("git", "-C", path, "rev-list", "--count", baseCommit+"..HEAD").Output()
		if cerr != nil {
			// Can't prove it's safe → don't touch it. Refusing to delete is always the recoverable
			// side of this decision.
			return false, "could not compare it to its base commit", nil
		}
		if n := strings.TrimSpace(string(out)); n != "" && n != "0" {
			return false, "it has " + n + " commit(s) not on its base branch", nil
		}
	}
	if rerr := Remove(repoDir, path, false); rerr != nil {
		return false, "git refused: " + rerr.Error(), rerr
	}
	_ = Prune(repoDir)
	return true, "", nil
}

// IsDirty reports whether a worktree has uncommitted changes (tracked or untracked).
func IsDirty(path string) (bool, error) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// SweepOrphans removes worktree directories under base whose git admin dir no longer exists — the
// residue of a repo that was deleted (or, in this codebase's case, of tests that created worktrees
// against a t.TempDir() repo that Go then cleaned up, leaving the worktree behind forever).
//
// The gitdir check is what makes this safe: a LIVE worktree's .git file points at a directory that
// resolves, so it can never match. Returns how many were removed.
func SweepOrphans(base string) (int, error) {
	repos, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		repoDir := filepath.Join(base, repo.Name())
		entries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			wt := filepath.Join(repoDir, e.Name())
			if !isOrphanWorktree(wt) {
				continue
			}
			if err := os.RemoveAll(wt); err == nil {
				removed++
			}
		}
		// Drop the per-repo dir once it's empty, so the base doesn't keep a tree of empty folders.
		if rest, err := os.ReadDir(repoDir); err == nil && len(rest) == 0 {
			_ = os.Remove(repoDir)
		}
	}
	return removed, nil
}

// isOrphanWorktree reports whether a directory is a git worktree whose admin dir is gone. Anything
// it cannot positively identify as an orphan is left alone.
func isOrphanWorktree(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return false // no .git file: not a worktree we made, so not ours to delete
	}
	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	if target == "" {
		return false
	}
	if _, err := os.Stat(target); err == nil {
		return false // admin dir still there → the worktree is live
	}
	return true
}

// Prune cleans stale worktree admin records (after a worktree dir was deleted manually).
func Prune(repoDir string) error {
	root, err := RepoRoot(repoDir)
	if err != nil {
		root = repoDir
	}
	return exec.Command("git", "-C", root, "worktree", "prune").Run()
}

// Slug lowercases name and reduces it to a filesystem/branch-safe token.
func Slug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
