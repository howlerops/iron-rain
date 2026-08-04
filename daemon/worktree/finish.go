package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// WouldConflict reports, NON-destructively, whether merging `base` (e.g. "main") into the
// worktree's current branch would conflict — without touching the working tree or leaving a
// mid-merge state (unlike CatchUp). It uses `git merge-tree --write-tree`, which computes the merge
// in memory and exits non-zero with the conflicted paths listed when there are conflicts. This is
// what drives the passive "conflict" status badge, so parallel agents on one repo don't silently
// collide. Returns the conflicted paths (empty = clean merge). base defaults to the repo's default
// branch when empty.
func WouldConflict(ctx context.Context, worktreePath, base string) ([]string, error) {
	if base == "" {
		base = DefaultBranch(worktreePath)
	}
	// merge-tree --write-tree: exit 0 = clean, exit 1 = conflicts (with an "Informational messages"
	// section listing conflicted files after a blank line), other = error. --name-only keeps the
	// conflict section to bare paths.
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge-tree", "--write-tree", "--name-only", "HEAD", base)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return nil, nil // clean merge
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		// Conflicts. Output format:
		//   <tree-oid>
		//   <conflicted file names, one per line>   (bare paths, via --name-only)
		//   <blank line>
		//   <human-readable informational messages>
		// The paths are the lines BEFORE the blank line, minus the leading OID line.
		out := outBuf.String()
		head := out
		if i := strings.Index(out, "\n\n"); i >= 0 {
			head = out[:i]
		}
		lines := strings.Split(strings.TrimSpace(head), "\n")
		var paths []string
		for _, ln := range lines[1:] { // skip the tree-OID line
			if p := strings.TrimSpace(ln); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			return []string{"(conflict)"}, nil // conflicted but couldn't parse paths
		}
		return paths, nil
	}
	return nil, fmt.Errorf("git merge-tree: %v: %s", err, strings.TrimSpace(errBuf.String()))
}

// Snapshot captures the worktree's CURRENT state (including uncommitted + untracked changes) as a
// git commit object WITHOUT modifying the working tree or index — a named restore point on the turn
// timeline. Returns the snapshot commit SHA. If the tree is clean it returns HEAD, so a checkpoint
// always resolves to something restorable.
func Snapshot(ctx context.Context, worktreePath string) (string, error) {
	// Build the snapshot in a TEMP index so the real index + working tree are never touched:
	//   read-tree HEAD → add -A (tracked + untracked) → write-tree → commit-tree.
	// This captures the exact current file contents without staging anything for the user.
	tmpIdx, err := os.CreateTemp("", "oculus-cp-index-*")
	if err != nil {
		return "", err
	}
	tmpIdx.Close()
	defer os.Remove(tmpIdx.Name())
	gitEnv := append(os.Environ(), "GIT_INDEX_FILE="+tmpIdx.Name())

	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
		cmd.Env = gitEnv
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}

	if _, err := run("read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	head, _ := HeadCommit(worktreePath)
	// Clean tree (snapshot tree == HEAD's tree) → no redundant commit; the restore point is HEAD.
	if headTree, err := run("rev-parse", "HEAD^{tree}"); err == nil && headTree == tree {
		return head, nil
	}
	args := []string{"commit-tree", tree, "-m", "oculus checkpoint"}
	if head != "" {
		args = []string{"commit-tree", tree, "-p", head, "-m", "oculus checkpoint"}
	}
	sha, err := run(args...)
	if err != nil {
		return "", err
	}
	// Keep the snapshot commit reachable so git gc can't drop it before a restore.
	_ = exec.CommandContext(ctx, "git", "-C", worktreePath, "update-ref", "refs/oculus/checkpoints/"+sha, sha).Run()
	return sha, nil
}

// RestoreSnapshot reverts the worktree's tracked files to the state captured in `sha` (from
// Snapshot). Untracked files created after the snapshot are left in place (non-destructive to new
// scratch files); tracked files are restored to the checkpoint content.
func RestoreSnapshot(ctx context.Context, worktreePath, sha string) error {
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "checkout", sha, "--", ".")
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git checkout %s: %v: %s", sha[:min(len(sha), 8)], err, strings.TrimSpace(errBuf.String()))
	}
	return nil
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

// MergeIntoDefault lands a worktree branch into the default branch of the MAIN checkout.
//
// Finishing a worktree used to offer exactly one destination — open a GitHub PR — so a repo with no
// remote hit a dead end: the agent's work sat on a branch with no way to land it from the phone.
//
// The merge runs with --no-ff so the branch stays legible in history, and it ABORTS on conflict
// rather than leaving a half-merged working tree for the user to discover later on their laptop.
// Conflicts are the user's call to make at a keyboard, not something to resolve blind from a phone.
func MergeIntoDefault(ctx context.Context, repoRoot, branch string) error {
	if branch == "" {
		return fmt.Errorf("no branch to merge")
	}
	base := DefaultBranch(repoRoot)
	// Refuse to touch a dirty main checkout: merging over uncommitted human work is not ours to do.
	if out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output(); err == nil {
		if strings.TrimSpace(string(out)) != "" {
			return fmt.Errorf("the main checkout has uncommitted changes — commit or stash them first")
		}
	}
	if out, err := exec.Command("git", "-C", repoRoot, "checkout", base).CombinedOutput(); err != nil {
		return fmt.Errorf("checkout %s: %v: %s", base, err, strings.TrimSpace(string(out)))
	}
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "merge", "--no-ff", "-m",
		"Merge "+branch, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Leave nothing behind. A merge left in progress is worse than a failed one: it turns up as a
		// broken repo the next time the user sits down.
		_ = exec.Command("git", "-C", repoRoot, "merge", "--abort").Run()
		return fmt.Errorf("merge %s into %s: %s", branch, base, strings.TrimSpace(string(out)))
	}
	return nil
}

// maxFailingChecks caps how many failing check names travel to the app. The point is to say WHAT
// broke at a glance on a phone-sized review screen, not to reproduce a whole CI matrix.
const maxFailingChecks = 5

// PRChecks summarizes a pull request's CI rollup. Counts and failing names are all a review screen
// can act on; the raw rollup is most of a page of JSON. Passed folds in NEUTRAL/SKIPPED runs because
// GitHub's own merge gate treats them as non-blocking.
type PRChecks struct {
	State   string   // SUCCESS | FAILURE | PENDING
	Passed  int
	Failed  int
	Pending int
	Failing []string // names of the failing checks, capped at maxFailingChecks
}

// PRInfo is a branch's pull-request state, URL and CI rollup. State is "" when there is no PR (or gh
// cannot answer), which is normal and not an error; Checks is nil when the PR has no checks at all —
// distinct from checks that exist but haven't reported yet.
type PRInfo struct {
	State  string // OPEN | MERGED | CLOSED
	URL    string
	Checks *PRChecks
}

// PRState reports a branch's pull-request state, so the app can tell the user their work actually
// landed instead of leaving the worktree around forever — and whether CI passed, because someone
// reviewing a worktree from their phone decides to merge off this one screen.
func PRState(ctx context.Context, worktreePath, branch string) (PRInfo, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return PRInfo{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "state,url,statusCheckRollup")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return PRInfo{}, nil // no PR for this branch is the common case, not a failure
	}
	return parsePRView(out)
}

// parsePRView reads `gh pr view --json state,url,statusCheckRollup` output. The rollup is a flat
// array of two different GraphQL node types — CheckRun (status + conclusion + name) and the older
// StatusContext (state + context) — and GitHub keeps adding conclusions to both, so a node that
// matches neither shape is skipped rather than failing the whole status call: a status screen that
// errors out is worse than one missing a check.
func parsePRView(data []byte) (PRInfo, error) {
	var res struct {
		State  string `json:"state"`
		URL    string `json:"url"`
		Rollup []struct {
			Name       string `json:"name"`       // CheckRun
			Status     string `json:"status"`     // CheckRun: QUEUED | IN_PROGRESS | COMPLETED | …
			Conclusion string `json:"conclusion"` // CheckRun, only once COMPLETED
			Context    string `json:"context"`    // StatusContext
			State      string `json:"state"`      // StatusContext
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return PRInfo{}, err
	}
	info := PRInfo{State: res.State, URL: res.URL}

	var checks PRChecks
	for _, n := range res.Rollup {
		name := n.Name
		if name == "" {
			name = n.Context
		}
		var verdict string
		switch {
		case n.Status != "" || n.Conclusion != "": // CheckRun
			// An unfinished run has no conclusion yet, so status decides; a run reported without a
			// status is judged on its conclusion alone.
			if n.Status == "" || strings.EqualFold(n.Status, "COMPLETED") {
				verdict = classifyConclusion(n.Conclusion)
			} else {
				verdict = "pending"
			}
		case n.State != "": // StatusContext
			verdict = classifyState(n.State)
		}
		switch verdict {
		case "pass":
			checks.Passed++
		case "fail":
			checks.Failed++
			if name != "" && len(checks.Failing) < maxFailingChecks {
				checks.Failing = append(checks.Failing, name)
			}
		case "pending":
			checks.Pending++
		}
	}
	if checks.Passed+checks.Failed+checks.Pending > 0 {
		switch {
		case checks.Failed > 0:
			// A failure outranks work still in flight: the pending runs can't un-fail it, and a green
			// "pending" badge over a broken build is exactly the lie this feature exists to prevent.
			checks.State = "FAILURE"
		case checks.Pending > 0:
			checks.State = "PENDING"
		default:
			checks.State = "SUCCESS"
		}
		info.Checks = &checks
	}
	return info, nil
}

// classifyConclusion maps a CheckRun conclusion. NEUTRAL/SKIPPED count as passing (GitHub's merge
// gate treats them as non-blocking); an unrecognised conclusion is dropped rather than guessed at.
func classifyConclusion(c string) string {
	switch strings.ToUpper(c) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "pass"
	case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return "fail"
	case "PENDING":
		return "pending"
	}
	return ""
}

// classifyState maps a StatusContext state (the older commit-status API).
func classifyState(s string) string {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "ERROR":
		return "fail"
	case "PENDING", "EXPECTED":
		return "pending"
	}
	return ""
}
