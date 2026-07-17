package worktree

import (
	"fmt"
	"os/exec"
	"strings"
)

// HeadCommit returns the current HEAD commit SHA of dir (used to pin a worktree's base
// so its diff is stable even if the main repo moves on).
func HeadCommit(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Diff returns the changes in worktreePath relative to baseRef (a commit/branch),
// including uncommitted work — what a reviewer wants to see before merging.
func Diff(worktreePath, baseRef string) (string, error) {
	args := []string{"-C", worktreePath, "diff"}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// CommitAll stages and commits everything in the worktree (no-op if clean). Returns
// whether a commit was made.
func CommitAll(worktreePath, message string) (bool, error) {
	if out, err := exec.Command("git", "-C", worktreePath, "add", "-A").CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Nothing staged? Then there's nothing to commit.
	if exec.Command("git", "-C", worktreePath, "diff", "--cached", "--quiet").Run() == nil {
		return false, nil
	}
	if out, err := exec.Command("git", "-C", worktreePath, "commit", "-m", message).CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// Push pushes branch from the worktree to origin, setting upstream.
func Push(worktreePath, branch string) error {
	if out, err := exec.Command("git", "-C", worktreePath, "push", "-u", "origin", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HasRemote reports whether the worktree's repo has an "origin" remote.
func HasRemote(worktreePath string) bool {
	return exec.Command("git", "-C", worktreePath, "remote", "get-url", "origin").Run() == nil
}

// CreatePR opens a GitHub PR for branch via the gh CLI, returning its URL. It requires
// gh on PATH and an origin remote; otherwise the caller should fall back to the agent
// harness (which has its own bash/gh) or a manual PR.
func CreatePR(worktreePath, branch, title, body string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh not found: push %s and open the PR manually, or ask the agent to run `gh pr create`", branch)
	}
	cmd := exec.Command("gh", "pr", "create", "--head", branch, "--title", title, "--body", body)
	cmd.Dir = worktreePath // gh operates on the repo in its working directory
	res, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %v: %s", err, strings.TrimSpace(string(res)))
	}
	return strings.TrimSpace(string(res)), nil
}
