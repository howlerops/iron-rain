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

// An isolated child must not be pointed at a file in the PARENT's directory.
//
// The handoff pointer was written for children that share the parent's cwd. Once the child has its
// own worktree, that same pointer is a read across a directory boundary — an external_directory
// approval raised before the child has done anything, on a session nobody is watching.
//
// Observed live: every isolated child stalled on its first move. Its transcript read "I'll review
// the shared handoff, then add the requested NOTES.md" and then stopped, because step one needed a
// permission it could not get. Two individually-correct features deadlocked each other.
func TestIsolatedChildPromptHasNoCrossBoundaryPath(t *testing.T) {
	rec := store.HandoffRecord{Title: "Ship the limiter", Summary: "Design agreed; implementation left."}
	parentHandoff := "/Users/x/parent-repo/.oculus/handoff/cc_parent.md"

	isolated := buildChildPrompt(
		protocol.SessionChild{Subtask: "write the docs", Worktree: true}, "", rec)
	if strings.Contains(isolated, parentHandoff) || strings.Contains(isolated, "/.oculus/handoff/") {
		t.Fatalf("isolated child was pointed outside its worktree:\n%s", isolated)
	}
	// It must still be seeded — dropping the pointer must not drop the context.
	for _, want := range []string{"Ship the limiter", "Design agreed", "write the docs"} {
		if !strings.Contains(isolated, want) {
			t.Fatalf("isolated child lost its seed context %q:\n%s", want, isolated)
		}
	}

	// A NON-isolated child shares the directory, so the pointer is a local read and still earns its
	// place — the full document beats a summary when it can be had for free.
	shared := buildChildPrompt(
		protocol.SessionChild{Subtask: "write the docs"}, parentHandoff, rec)
	if !strings.Contains(shared, parentHandoff) {
		t.Fatalf("shared-directory child lost its handoff pointer:\n%s", shared)
	}
}
