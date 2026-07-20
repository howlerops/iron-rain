// Package worktree automates git worktrees so each session can run on its own isolated
// branch (the pattern every worktree ADE uses). The daemon owns the lifecycle: create a
// worktree on a fresh branch, remove it, and prune stale admin records. Worktrees live
// OUTSIDE the repo (default ~/.oculus/worktrees/<repo>/<name>) to avoid nested-git and
// repo pollution.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is a created worktree: its checkout path and the branch it's on.
type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// RepoRoot returns the git top-level of dir, or an error if dir isn't in a repo.
func RepoRoot(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		// Surface git's real stderr (missing git, permissions, corrupt repo) rather
		// than flattening every failure to "not a git repository".
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: not a git repository: %w: %s", dir, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: not a git repository: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
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
	if out, err := exec.Command("git", "-C", root, "worktree", "add", path, "-b", branch).CombinedOutput(); err != nil {
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
