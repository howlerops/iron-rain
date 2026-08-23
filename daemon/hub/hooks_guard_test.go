package hub

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// The attack this exists to stop, end to end: an agent writes a hook into its own worktree, the user
// approves what looks like an ordinary file write, and the daemon's OWN `git commit`/`git merge` at
// finish time executes it as the owner — reachable from a phone via the relay.
func TestGuardRefusesWritesIntoGitMetadata(t *testing.T) {
	wt := t.TempDir()
	cases := []struct {
		name, path string
	}{
		{"pre-commit hook", filepath.Join(wt, ".git", "hooks", "pre-commit")},
		{"post-merge hook", filepath.Join(wt, ".git", "hooks", "post-merge")},
		{"git config", filepath.Join(wt, ".git", "config")},
		{"the worktree's own .git file", filepath.Join(wt, ".git")},
		{"mercurial too", filepath.Join(wt, ".hg", "hgrc")},
		// Normalisation must judge where the path LANDS, not how it is spelled.
		{"traversal into .git", filepath.Join(wt, "src", "..", ".git", "hooks", "pre-push")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := guardApproval(protocol.ApprovalRequest{Tool: "write", Detail: tc.path})
			if g.reason == "" {
				t.Fatalf("allowed a write to %s — this is the deferred-shell path", tc.path)
			}
		})
	}
}

// Ordinary work must be untouched. A guard that fires on normal edits would be worse than the hole:
// people would turn it off.
func TestGuardAllowsOrdinaryWrites(t *testing.T) {
	wt := t.TempDir()
	for _, p := range []string{
		filepath.Join(wt, "main.go"),
		filepath.Join(wt, "src", "app", "index.ts"),
		filepath.Join(wt, ".github", "workflows", "ci.yml"), // .github is NOT .git
		filepath.Join(wt, "docs", "gitignore-notes.md"),
		filepath.Join(wt, ".gitignore"),
	} {
		if g := guardApproval(protocol.ApprovalRequest{Tool: "write", Detail: p}); g.reason != "" {
			t.Fatalf("refused an ordinary write to %s: %s", p, g.reason)
		}
	}
}

// opencode sends the target in Patterns rather than Detail, so checking Detail alone would leave the
// hole open for one of the providers that matters most.
func TestGuardChecksPatternsNotJustDetail(t *testing.T) {
	wt := t.TempDir()
	ar := protocol.ApprovalRequest{
		Tool:     "edit",
		Detail:   "editing a file",
		Patterns: []string{filepath.Join(wt, ".git", "hooks", "pre-commit")},
	}
	if g := guardApproval(ar); g.reason == "" {
		t.Fatal("allowed a metadata write that arrived via Patterns")
	}
}

// The path rule alone is not enough: core.hooksPath redirects git's hooks to a directory the agent
// controls, so the hook file itself never has to be called .git/anything. An agent can write
// .oculus-tools/pre-commit — an unremarkable path — and point git at it.
func TestGuardRefusesHooksPathRedirect(t *testing.T) {
	for _, cmd := range []string{
		"git config core.hooksPath .oculus-tools",
		"git config --local core.hooksPath ./tools",
		"git -C /tmp/wt config core.hookspath hooks", // git treats the option case-insensitively
		"cd /tmp/wt && git config core.hooksPath .ci && echo done",
	} {
		if g := guardApproval(protocol.ApprovalRequest{Tool: "bash", Detail: cmd}); g.reason == "" {
			t.Fatalf("allowed a hooksPath redirect: %q", cmd)
		}
	}
}

// Normal git usage must keep working — this rule is narrow on purpose.
func TestGuardAllowsOrdinaryGitCommands(t *testing.T) {
	for _, cmd := range []string{
		"git status",
		"git config user.email t@t",
		"git commit -m 'add hooks documentation'",
		"go test ./...",
		"git log --oneline",
	} {
		if g := guardApproval(protocol.ApprovalRequest{Tool: "bash", Detail: cmd}); g.reason != "" {
			t.Fatalf("refused an ordinary command %q: %s", cmd, g.reason)
		}
	}
}

// A shell command is not a path. Treating Detail as one would judge "git" rather than anything real,
// and could refuse innocuous commands for containing a slash.
func TestGuardDoesNotTreatCommandLinesAsPaths(t *testing.T) {
	ar := protocol.ApprovalRequest{Tool: "bash", Detail: "ls -la /tmp/project/src"}
	if g := guardApproval(ar); g.reason != "" {
		t.Fatalf("refused a command line as if it were a path: %s", g.reason)
	}
}

// The reason reaches the user, so it has to say what happened and why — "blocked" alone sends people
// looking for a bug in their agent.
func TestGuardReasonExplainsTheConsequence(t *testing.T) {
	g := guardApproval(protocol.ApprovalRequest{
		Tool: "write", Detail: filepath.Join(t.TempDir(), ".git", "hooks", "pre-commit")})
	if g.reason == "" {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"hook", "git"} {
		if !containsFold(g.reason, want) {
			t.Fatalf("reason %q does not mention %q", g.reason, want)
		}
	}
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// The regression that live testing caught and the unit tests missed.
//
// The first version of this guard checked only Detail and Patterns. Both unit tests passed, because
// they fed Detail the path directly. The real claude-code harness puts the target in Input — the
// tool's raw arguments — so an actual attempt to write .git/hooks/pre-commit went straight through
// to the approval card. A guard is only as good as the field the harness actually uses.
func TestGuardChecksTheToolsRawInput(t *testing.T) {
	wt := t.TempDir()
	ar := protocol.ApprovalRequest{
		Tool:   "Write",
		Detail: "Write", // a summary, NOT a path — this is the shape that defeated the first version
		Input: []byte(`{"file_path":"` + filepath.Join(wt, ".git", "hooks", "pre-commit") +
			`","content":"#!/bin/sh\necho pwned"}`),
	}
	if g := guardApproval(ar); g.reason == "" {
		t.Fatal("allowed a hook write that arrived in Input — the live bypass")
	}
}

// A batch edit must not smuggle a metadata path past a top-level-only scan.
func TestGuardChecksNestedInputPaths(t *testing.T) {
	wt := t.TempDir()
	ar := protocol.ApprovalRequest{
		Tool:   "MultiEdit",
		Detail: "MultiEdit",
		Input: []byte(`{"edits":[{"file_path":"` + filepath.Join(wt, "ok.go") + `"},` +
			`{"file_path":"` + filepath.Join(wt, ".git", "config") + `"}]}`),
	}
	if g := guardApproval(ar); g.reason == "" {
		t.Fatal("allowed a metadata path nested inside a batch edit")
	}
}

// Ordinary tool input must still pass, including prompts that merely mention .git in prose.
func TestGuardAllowsOrdinaryInput(t *testing.T) {
	wt := t.TempDir()
	for _, in := range []string{
		`{"file_path":"` + filepath.Join(wt, "main.go") + `","content":"package main"}`,
		`{"prompt":"explain how .git/hooks works in this repo"}`,
		`{"command":"git status"}`,
	} {
		ar := protocol.ApprovalRequest{Tool: "Write", Detail: "Write", Input: []byte(in)}
		if g := guardApproval(ar); g.reason != "" {
			t.Fatalf("refused ordinary input %s: %s", in, g.reason)
		}
	}
}
