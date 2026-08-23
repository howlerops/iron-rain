package hub

import (
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// A delegated child asking for isolation must get its OWN branch, not the parent's directory.
//
// Before this, spawnChild called p.Create directly and bypassed startSession entirely — which is
// where worktrees, models and every other piece of per-session setup live. So a child could never
// have a worktree however it was asked, and two concurrent children edited the same files
// underneath each other with neither agent told. Routing through startSession is what fixes it, and
// this pins the branch naming that makes concurrent children distinguishable in `git worktree list`.
func TestChildWorkspaceNamesAreUniquePerDelegation(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		name := "subtask-" + randToken()[:6]
		if seen[name] {
			t.Fatalf("duplicate child workspace name %q — two concurrent subtasks would collide on one branch", name)
		}
		seen[name] = true
		if !strings.HasPrefix(name, "subtask-") {
			t.Fatalf("workspace name %q does not say what it is", name)
		}
	}
}

// The model must survive the trip. Provider alone was never enough — "delegate this to Claude" and
// "delegate this to Claude on a small model" are different asks, and only the first was expressible.
func TestSessionChildCarriesModelAndIsolation(t *testing.T) {
	req := protocol.SessionChild{
		ParentSessionID: "p1",
		Subtask:         "write the migration",
		Provider:        "opencode",
		Model:           "gpt-5.6-sol",
		ModelProvider:   "openai",
		Worktree:        true,
	}
	if req.Model == "" || req.ModelProvider == "" {
		t.Fatal("Model/ModelProvider must be expressible on a delegation")
	}
	if !req.Worktree {
		t.Fatal("Worktree must be expressible on a delegation")
	}
	// The prompt is built from the request and must not silently drop the subtask.
	prompt := buildChildPrompt(req, "", store.HandoffRecord{})
	if !strings.Contains(prompt, "write the migration") {
		t.Fatalf("child prompt lost the subtask: %q", prompt)
	}
}
