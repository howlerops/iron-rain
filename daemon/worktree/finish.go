package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitNetworkTimeout bounds git/gh operations that touch the network (push, PR
// creation) so a stalled remote can't block a session teardown indefinitely.
const gitNetworkTimeout = 5 * time.Minute

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
func Diff(ctx context.Context, worktreePath, baseRef string) (string, error) {
	args := []string{"-C", worktreePath, "diff"}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	// Keep stdout (the diff) and stderr (git warnings/advice) separate so warnings
	// like "LF will be replaced by CRLF" don't get interleaved into the diff text.
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return outBuf.String(), nil
}

// CommitAll stages and commits everything in the worktree (no-op if clean). Returns
// whether a commit was made.
func CommitAll(ctx context.Context, worktreePath, message string) (bool, error) {
	if out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", "-A").CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Nothing staged? Then there's nothing to commit.
	if exec.CommandContext(ctx, "git", "-C", worktreePath, "diff", "--cached", "--quiet").Run() == nil {
		return false, nil
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "commit", "-m", message).CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// Push pushes branch from the worktree to origin, setting upstream.
func Push(ctx context.Context, worktreePath, branch string) error {
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "-u", "origin", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DefaultBranch resolves the repo's default branch (what "main" means for this repo): origin/HEAD
// when set, else the first of main/master/develop that exists, else "main".
func DefaultBranch(dir string) string {
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out)) // e.g. "origin/main"
		if i := strings.IndexByte(ref, '/'); i >= 0 {
			return ref[i+1:]
		}
	}
	for _, b := range []string{"main", "master", "develop"} {
		if exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b).Run() == nil {
			return b
		}
	}
	return "main"
}

// CatchUpResult reports the outcome of merging the default branch into a session's branch.
type CatchUpResult struct {
	Status    string   // "updated" | "up_to_date" | "conflicts"
	Base      string   // the default branch merged in (e.g. "main")
	Message   string   // human summary
	Conflicts []string // conflicted paths (Status=="conflicts"); the merge is left in progress to resolve
}

// CatchUpToMain fetches origin's default branch and MERGES it into the worktree's branch, keeping a
// long-running session branch current. Merge (not rebase) is deliberate: no history rewrite, and any
// conflicts land in-tree for the agent/user to resolve. If the merge conflicts, the worktree is left
// mid-merge (so it can be resolved) and the conflicted files are returned. Uncommitted changes block
// the merge — the caller should commit first.
func CatchUpToMain(ctx context.Context, worktreePath string) (CatchUpResult, error) {
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	base := DefaultBranch(worktreePath)

	// A dirty tree can't be merged into cleanly — surface it instead of a cryptic git error.
	if exec.CommandContext(ctx, "git", "-C", worktreePath, "diff", "--quiet").Run() != nil ||
		exec.CommandContext(ctx, "git", "-C", worktreePath, "diff", "--cached", "--quiet").Run() != nil {
		return CatchUpResult{Status: "conflicts", Base: base,
			Message: "You have uncommitted changes — commit them first, then catch up to " + base + "."}, nil
	}

	if out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", "origin", base).CombinedOutput(); err != nil {
		return CatchUpResult{}, fmt.Errorf("git fetch origin %s: %v: %s", base, err, strings.TrimSpace(string(out)))
	}
	// Already contains origin/base? Then there's nothing to merge.
	if exec.CommandContext(ctx, "git", "-C", worktreePath, "merge-base", "--is-ancestor", "origin/"+base, "HEAD").Run() == nil {
		return CatchUpResult{Status: "up_to_date", Base: base, Message: "Already up to date with " + base + "."}, nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge", "--no-edit", "origin/"+base).CombinedOutput()
	if err != nil {
		// Conflicts: git exits non-zero and leaves the merge in progress. Collect the conflicted
		// files so the app can show them; the agent (or user) resolves + commits.
		files, _ := conflictedFiles(worktreePath)
		if len(files) > 0 {
			return CatchUpResult{Status: "conflicts", Base: base, Conflicts: files,
				Message: fmt.Sprintf("Merged %s but %d file(s) conflict — resolve them, then commit.", base, len(files))}, nil
		}
		return CatchUpResult{}, fmt.Errorf("git merge origin/%s: %v: %s", base, err, strings.TrimSpace(string(out)))
	}
	return CatchUpResult{Status: "updated", Base: base, Message: "Caught up to " + base + "."}, nil
}

func conflictedFiles(worktreePath string) ([]string, error) {
	out, err := exec.Command("git", "-C", worktreePath, "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// HasRemote reports whether the worktree's repo has an "origin" remote.
func HasRemote(worktreePath string) bool {
	return exec.Command("git", "-C", worktreePath, "remote", "get-url", "origin").Run() == nil
}

// CreatePR opens a GitHub PR for branch via the gh CLI, returning its URL. It requires
// gh on PATH and an origin remote; otherwise the caller should fall back to the agent
// harness (which has its own bash/gh) or a manual PR.
func CreatePR(ctx context.Context, worktreePath, branch, title, body string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh not found: push %s and open the PR manually, or ask the agent to run `gh pr create`", branch)
	}
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "create", "--head", branch, "--title", title, "--body", body)
	cmd.Dir = worktreePath // gh operates on the repo in its working directory
	res, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %v: %s", err, strings.TrimSpace(string(res)))
	}
	return strings.TrimSpace(string(res)), nil
}

// ChangedFiles returns the paths changed in worktreePath relative to baseRef (committed
// + uncommitted). Used to detect cross-worktree overlaps.
func ChangedFiles(worktreePath, baseRef string) ([]string, error) {
	args := []string{"-C", worktreePath, "diff", "--name-only"}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// Overlaps returns, for each file changed by `target`, the OTHER keys (branches/labels)
// in changed that also touched it — the cross-worktree conflict warning: two agents
// editing the same file on different branches will collide on merge.
func Overlaps(target string, changed map[string][]string) map[string][]string {
	inTarget := map[string]bool{}
	for _, f := range changed[target] {
		inTarget[f] = true
	}
	result := map[string][]string{}
	for label, files := range changed {
		if label == target {
			continue
		}
		for _, f := range files {
			if inTarget[f] {
				result[f] = append(result[f], label)
			}
		}
	}
	return result
}
