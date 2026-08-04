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
func DefaultBase() string {
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

// createAt adds a worktree for an already-resolved repo root at an explicit path on branch.
// Shared by Create (single repo) and CreateWorkspace (one call per member repo).
func createAt(root, path, branch string) (Worktree, error) {
	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("worktree path already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}
	// git worktree add <path> -b <branch> creates the branch from the current HEAD.
	ctx, cancel := gitContext()
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", path, "-b", branch).CombinedOutput(); err != nil {
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
