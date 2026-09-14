package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	// An MCP tool arrives qualified as `mcp:<server>:<tool>` so a rule can name one server's tool
	// without colliding with a native one. The guard wants the bare name: a shell exposed by an MCP
	// server is still a shell, and `mcp:shelly:bash` must match isShellTool exactly as `bash` does.
	if rest, ok := strings.CutPrefix(tool, "mcp:"); ok {
		if _, bare, found := strings.Cut(rest, ":"); found {
			tool = bare
		}
	}

	// Any tool that names a path: refuse the ones that land in repository metadata. Checked for
	// EVERY tool rather than a write allowlist, because a tool we haven't seen is exactly the one
	// whose behaviour we can't predict — and a harness that invents `SaveFile` should not get a pass
	// that `write` doesn't.
	for _, cand := range approvalPaths(ar) {
		if meta := fsaccess.VCSMetadataComponent(cand); meta != "" {
			return approvalGuard{reason: "writes into " + meta + " are refused — a hook placed there " +
				"would be executed by Iron Rain's own git commit/merge when you finish this session"}
		}
		// The same standard, applied to the rest of the paths that end in execution by somebody other
		// than the agent. fsaccess already knows them — it is the list the daemon's OWN file
		// operations are held to — but approvals never consulted it, so an agent's own Write tool
		// reached everything the daemon's file browser is forbidden from touching.
		//
		// The .git argument transfers exactly. ~/.oculus/agents.json defines custom CLI agents as
		// commands THIS DAEMON executes, so writing it is indistinguishable from writing a hook.
		// ~/Library/LaunchAgents runs at login. A shell rc file runs on every new shell — including
		// the login shell the daemon itself uses to resolve PATH. In each case the write looks
		// ordinary on a one-line card and the execution arrives later, from us, which is the precise
		// reason the .git rule refuses rather than asks.
		if label := fsaccess.ProtectedPath(cand); label != "" {
			return approvalGuard{reason: "writes into " + label + " are refused — it is executed later " +
				"by Iron Rain, by launchd or by your shell, so approving it here would be approving " +
				"code you cannot see on this card"}
		}
	}

	// Redirecting hooksPath achieves the same thing without ever naming .git, so the path rule alone
	// is not enough. Only shell-ish tools can run it.
	//
	// Scanned across Detail AND the raw arguments for the same reason the path check is: Detail is a
	// human-readable summary each harness writes however it likes, and an MCP server's Detail is
	// generated by US ("bash (via the shelly MCP server)") — it cannot contain the command, which
	// arrives only in the arguments. Checking Detail alone would leave every MCP shell unguarded.
	if isShellTool(tool) && hooksPathRe.MatchString(ar.Detail+" "+string(ar.Input)) {
		return approvalGuard{reason: "changing core.hooksPath is refused — it redirects git's hooks " +
			"to a directory the agent controls, which Iron Rain's own git commit/merge would then execute"}
	}

	// A shell command's own TEXT, for the same rule. approvalPaths deliberately reads only the
	// pathKeys arguments, and `command` is not one of them — on purpose, so a prompt or a commit
	// message that mentions .git does not refuse the operation. But that left the redirect wide
	// open: `printf '…' > .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit` names no
	// file_path, matches no pattern, and does not mention core.hooksPath, so the guard returned
	// empty and yolo mode auto-allowed it. The daemon's own `git commit` (no --no-verify) then runs
	// it as the owner — the exact chain this file exists to break.
	//
	// Refusing a shell command that merely READS .git is consistent, not stricter: fsaccess.Resolve
	// already refuses reads of repository metadata as well as writes.
	if isShellTool(tool) {
		if label, tok := shellTouchesProtected(ar.Detail + " " + string(ar.Input)); label != "" {
			return approvalGuard{reason: "this command touches " + label + " (" + tok + ") — refused " +
				"for the same reason a direct write is: it is executed later by Iron Rain, by launchd " +
				"or by your shell, so approving it here would be approving code you cannot see"}
		}
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
// All THREE sources are checked, and Input is the one that matters most: it is the tool's raw
// arguments, i.e. the thing the harness will actually act on. Detail is a human-readable summary
// whose content is up to each harness, and Patterns is opencode's permission scoping. A guard built
// on Detail alone passes its unit tests and then fails in production, because the test feeds Detail
// the path while the real harness puts it in Input — which is exactly what happened here: a live
// attempt to write .git/hooks/pre-commit sailed through to the approval card.
func approvalPaths(ar protocol.ApprovalRequest) []string {
	out := make([]string, 0, len(ar.Patterns)+4)
	if d := strings.TrimSpace(ar.Detail); d != "" && !strings.Contains(d, "\n") {
		// A Detail that is a whole shell command is not a path; taking its first token would judge
		// `git` rather than anything real. Only treat it as a path when it looks like one.
		if looksLikePath(d) {
			out = append(out, d)
		}
	}
	out = append(out, ar.Patterns...)
	out = append(out, inputPaths(ar.Input)...)
	return out
}

// pathKeys are the argument names harnesses use for "the file this operates on". Kept as a list
// rather than "any string that looks like a path" so a prompt or a commit message that happens to
// mention .git does not refuse the operation.
var pathKeys = []string{
	"file_path", "filePath", "path", "notebook_path", "notebookPath",
	"target_file", "targetFile", "old_path", "new_path", "destination", "dest", "source", "src",
	// MCP names its target `uri`, and a resources/read is a file read by another name. Without this
	// the guard had nothing to inspect on that method at all.
	"uri",
}

// inputPaths pulls the filesystem targets out of a tool's raw arguments, including one level of
// nesting (harnesses wrap edits in an "edits"/"files" array) so a batch edit cannot smuggle a path
// past a top-level-only scan.
func inputPaths(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	var out []string
	collect := func(m map[string]any) {
		for _, k := range pathKeys {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, unwrapFileURI(s))
			}
		}
	}
	collect(obj)
	for _, v := range obj {
		nested, ok := v.([]any)
		if !ok {
			continue
		}
		for _, item := range nested {
			if m, ok := item.(map[string]any); ok {
				collect(m)
			}
		}
	}
	return out
}

// unwrapFileURI turns a file:// URI into the plain path it names, and leaves anything else alone.
// MCP resources are addressed by URI, so without this every path check below would be comparing
// against a string that begins with a scheme and matching nothing.
func unwrapFileURI(s string) string {
	for _, prefix := range []string{"file://localhost/", "file:///", "file://"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			if strings.HasPrefix(prefix, "file:///") || prefix == "file://localhost/" {
				return "/" + rest
			}
			return rest
		}
	}
	return s
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

// shellTouchesProtected scans a shell command for a token naming a protected location, and returns
// the label plus the offending token.
//
// Tokenised on shell separators rather than whitespace alone, so `>.git/hooks/pre-commit` and
// `cp x .git/config;` are seen as paths. A token is judged by ProtectedPath and by the VCS-metadata
// rule, the same two checks a declared file argument gets — the point is that a shell command
// reaches the identical destinations by another route.
func shellTouchesProtected(cmd string) (label, token string) {
	if cmd == "" {
		return "", ""
	}
	for _, tok := range strings.FieldsFunc(cmd, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '"', '\'', '`', '>', '<', '|', ';', '&', '(', ')', ',':
			return true
		}
		return false
	}) {
		tok = strings.TrimSpace(tok)
		if tok == "" || !strings.ContainsAny(tok, "/\\") {
			continue // a bare word cannot be a path into a metadata directory
		}
		// A shell expands ~ before the command runs, so the guard has to as well — otherwise
		// `> ~/.oculus/agents.json` is judged as a relative path called "~" and matches nothing.
		if tok == "~" || strings.HasPrefix(tok, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				tok = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(tok, "~"), "/"))
			}
		}
		if l := fsaccess.ProtectedPath(tok); l != "" {
			return l, tok
		}
		if name := fsaccess.VCSMetadataComponent(tok); name != "" {
			return name, tok
		}
	}
	return "", ""
}
