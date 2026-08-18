package hub

import (
	"os"
	"testing"
)

// TestMain contains this package's worktree creation inside the test run's own temp space.
//
// Fanout and checkpoint tests create real git worktrees, and any that doesn't pass an explicit base
// gets worktree.DefaultBase() — the developer's actual ~/.oculus/worktrees. The repos those
// worktrees belong to are t.TempDir()s that Go removes when the test ends, but a worktree created
// OUTSIDE that directory is nobody's job to clean up, so it stays forever pointing at a repo that
// no longer exists. A dev box that had run the suite a few hundred times held 1133 of them.
//
// Setting the base here fixes the whole package at once, including tests written later that forget.
func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "oculus-worktrees-test")
	if err != nil {
		panic("hub tests: could not create a temp worktree base: " + err.Error())
	}
	_ = os.Setenv("OCULUS_WORKTREE_BASE", base)
	code := m.Run()
	// Best-effort: worktrees hold no state worth keeping past the run, and a failure to clean up
	// must not turn a passing suite red.
	_ = os.RemoveAll(base)
	os.Exit(code)
}
