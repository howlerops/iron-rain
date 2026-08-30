package agent

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The branch a session is working on belongs in its status bar — every provider's own TUI shows it,
// and "which branch is this agent editing" is a question you want answered before you read a diff,
// not after. No provider reports it, because it is a property of the working directory rather than
// of the conversation, so it is read here once per provider rather than four times differently.

// branchCache avoids running git on every facts report. A session's branch changes rarely, and a
// stale answer for a few seconds is a far better trade than a subprocess per status update on a
// daemon that may be driving dozens of sessions.
var branchCache sync.Map // cwd -> branchEntry

type branchEntry struct {
	branch string
	at     time.Time
}

const branchTTL = 10 * time.Second

// GitBranch returns the current branch for a working directory, or "" when it is not a repository,
// git is missing, or the repo is in a detached head. Never returns an error: a status bar has
// nothing useful to do with one, and a missing branch simply renders as nothing.
func GitBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	if v, ok := branchCache.Load(cwd); ok {
		if e, ok := v.(branchEntry); ok && time.Since(e.at) < branchTTL {
			return e.branch
		}
	}
	// --quiet so a detached head exits non-zero rather than printing "HEAD", which would otherwise
	// be shown to the user as if it were a branch name.
	cmd := exec.Command("git", "-C", cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	out, err := cmd.Output()
	branch := ""
	if err == nil {
		branch = strings.TrimSpace(string(out))
	}
	branchCache.Store(cwd, branchEntry{branch: branch, at: time.Now()})
	return branch
}
