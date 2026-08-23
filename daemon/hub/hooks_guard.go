package hub

import (
	"regexp"
	"strings"

	"github.com/howlerops/oculus/daemon/fsaccess"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Repository metadata is off limits to agents, and this is a SECURITY rule rather than a tidiness
// one: a write into .git turns into code execution later, performed by us.
//
// The chain, end to end:
//
//  1. An agent writes .git/hooks/pre-commit inside its own worktree. The path is inside the
//     worktree, so every prefix-based check passes, and the approval card shows what reads as an
//     ordinary file write.
//  2. The user picks that variant and hits Finish.
//  3. The DAEMON runs `git add -A && git commit` (worktree/finish.go), or `git merge` on the main
//     checkout, or `git push`. None of them pass --no-verify.
//  4. Git executes the hook as the owner.
//
// The relay makes step 1 reachable from a phone anywhere, which is precisely the standard that got
// encrypted PTY sessions parked: a capability that ends in arbitrary code execution, reachable
// remotely, cannot be handed over on a card that doesn't look like it. The only thing separating
// this from a shell is the delay — and the delay makes it harder to notice, not safer.
//
// Refused rather than merely surfaced, because the disclosure people would rely on doesn't hold.
// `core.hooksPath` can point anywhere, so the file need not be called .git/hooks/anything: an agent
// can write .oculus-tools/pre-commit — an unremarkable-looking path — and redirect hooks to it with
// a `git config` call that reads as routine housekeeping.
//
// Legitimate hooks are deliberately left working. The alternative fix (--no-verify on every
// daemon-initiated git operation) closes the same hole, but it silently stops a repo's real
// pre-commit hooks — formatters, linters — from running on agent commits, which changes what lands.
// Blocking the write keeps honest repos behaving exactly as they did.
type approvalGuard struct {
	// reason is empty when the request is allowed to proceed to the user.
	reason string
}

// hooksPathRe matches an attempt to redirect git's hook directory. Deliberately broad about the
// surrounding command: this appears as `git config core.hooksPath X`, `git -C dir config --local
// core.hooksPath X`, and inside compound shell lines, and the option is case-insensitive to git.
var hooksPathRe = regexp.MustCompile(`(?i)\bcore\.hookspath\b`)

// guardApproval judges a request BEFORE it is auto-allowed or shown to the user. A non-empty reason
// means deny outright: these are not decisions a person should be asked to make on a one-line card,
// because the consequence is invisible at the moment of asking and arrives later, from us.
func guardApproval(ar protocol.ApprovalRequest) approvalGuard {
	tool := strings.ToLower(strings.TrimSpace(ar.Tool))
	tool = strings.TrimPrefix(tool, "[sub-agent] ")

	// Any tool that names a path: refuse the ones that land in repository metadata. Checked for
	// EVERY tool rather than a write allowlist, because a tool we haven't seen is exactly the one
	// whose behaviour we can't predict — and a harness that invents `SaveFile` should not get a pass
	// that `write` doesn't.
	for _, cand := range approvalPaths(ar) {
		if meta := fsaccess.VCSMetadataComponent(cand); meta != "" {
			return approvalGuard{reason: "writes into " + meta + " are refused — a hook placed there " +
				"would be executed by Iron Rain's own git commit/merge when you finish this session"}
		}
	}

	// Redirecting hooksPath achieves the same thing without ever naming .git, so the path rule alone
	// is not enough. Only shell-ish tools can run it.
	if isShellTool(tool) && hooksPathRe.MatchString(ar.Detail) {
		return approvalGuard{reason: "changing core.hooksPath is refused — it redirects git's hooks " +
			"to a directory the agent controls, which Iron Rain's own git commit/merge would then execute"}
	}
	return approvalGuard{}
}

// isShellTool reports whether a tool executes a command line, so the hooksPath rule is applied to
// the things that could actually run it rather than to every Detail string in the system.
func isShellTool(tool string) bool {
	switch tool {
	case "bash", "shell", "run", "execute", "terminal", "command", "sh", "zsh":
		return true
	}
	return false
}

// approvalPaths returns the filesystem paths a request refers to.
//
// Detail is where every harness puts the target of a file operation, and Patterns is what opencode
// sends for permission scoping. Both are checked: an entry in either that reaches into repository
// metadata is enough to refuse, since we cannot tell which one the harness will actually act on.
func approvalPaths(ar protocol.ApprovalRequest) []string {
	out := make([]string, 0, len(ar.Patterns)+1)
	if d := strings.TrimSpace(ar.Detail); d != "" && !strings.ContainsAny(d, "\n") {
		// A Detail that is a whole shell command is not a path; taking its first token would judge
		// `git` rather than anything real. Only treat it as a path when it looks like one.
		if looksLikePath(d) {
			out = append(out, d)
		}
	}
	out = append(out, ar.Patterns...)
	return out
}

// looksLikePath is intentionally conservative: it is used to decide whether to APPLY a restriction,
// so a false negative merely means the Patterns check carries the weight, while a false positive
// would refuse an innocuous command for containing a slash.
func looksLikePath(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false // a command line, not a path
	}
	return strings.Contains(s, "/")
}
