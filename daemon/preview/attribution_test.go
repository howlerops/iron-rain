package preview

import "testing"

// A parent session must not claim a nested worktree's dev server.
//
// Without deepest-match a session opened on the repo root matches every listener beneath it, so the
// parent's preview URL silently serves a child worktree's app — one session's name pointing at
// another session's server, which is the exact confusion the feature exists to remove.
func TestDeepestDirectoryOwnsTheListener(t *testing.T) {
	ls := []listener{{pid: 1, port: 5173, cwd: "/repo/worktrees/feature-a"}}
	dirs := map[string]string{
		"parent": "/repo",
		"child":  "/repo/worktrees/feature-a",
	}
	got := attribute(ls, dirs)
	if _, claimed := got["parent"]; claimed {
		t.Fatalf("the parent session claimed the child's server: %v", got)
	}
	if got["child"] != 5173 {
		t.Fatalf("child got %v, want 5173", got["child"])
	}
}

// Sessions on the SAME directory are genuinely both owners — picking one would make the result
// depend on map iteration order.
func TestIdenticalDirectoriesBothKeepThePort(t *testing.T) {
	ls := []listener{{pid: 1, port: 3000, cwd: "/repo"}}
	dirs := map[string]string{"a": "/repo", "b": "/repo"}
	got := attribute(ls, dirs)
	if got["a"] != 3000 || got["b"] != 3000 {
		t.Fatalf("both sessions should keep the shared port, got %v", got)
	}
}

// A conventional dev port beats a lower ephemeral one. Lowest-alone would hand a session its
// debugger instead of its app.
func TestDevPortBeatsALowerEphemeralPort(t *testing.T) {
	ls := []listener{
		{pid: 1, port: 4999, cwd: "/repo"}, // lower, but not a dev port
		{pid: 2, port: 5173, cwd: "/repo"}, // Vite
	}
	got := attribute(ls, map[string]string{"s": "/repo"})
	if got["s"] != 5173 {
		t.Fatalf("chose :%d, want :5173 — a debugger would outrank the app", got["s"])
	}
}

// Between two conventional ports, the lower one wins (3000 over 8080 is the usual app-vs-tooling
// split).
func TestLowerDevPortWinsBetweenTwoConventionalOnes(t *testing.T) {
	ls := []listener{
		{pid: 1, port: 8080, cwd: "/repo"},
		{pid: 2, port: 3000, cwd: "/repo"},
	}
	if got := attribute(ls, map[string]string{"s": "/repo"}); got["s"] != 3000 {
		t.Fatalf("chose :%d, want :3000", got["s"])
	}
}

// With no conventional port at all, fall back to the lowest.
func TestFallsBackToLowestWhenNothingIsConventional(t *testing.T) {
	ls := []listener{
		{pid: 1, port: 41000, cwd: "/repo"},
		{pid: 2, port: 39000, cwd: "/repo"},
	}
	if got := attribute(ls, map[string]string{"s": "/repo"}); got["s"] != 39000 {
		t.Fatalf("chose :%d, want :39000", got["s"])
	}
}

func TestBetterRanking(t *testing.T) {
	cases := []struct {
		cand, cur int
		want      bool
	}{
		{5173, 4999, true},  // dev port beats lower non-dev
		{4999, 5173, false}, // non-dev never beats a dev port
		{3000, 8080, true},  // lower dev port wins
		{8080, 3000, false},
		{39000, 41000, true}, // neither conventional: lower wins
	}
	for _, c := range cases {
		if got := better(c.cand, c.cur); got != c.want {
			t.Errorf("better(%d, %d) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

// The agent machinery listens inside project directories and must never be named as the user's app.
// `opencode serve` runs with cwd set to the project it serves; the daemon does the same from a
// checkout.
func TestInfrastructureIsExcluded(t *testing.T) {
	for _, cmd := range []string{"opencode", "oculusd"} {
		if !infraCommands[cmd] {
			t.Errorf("%s is not excluded — a session in that project would name the agent harness", cmd)
		}
	}
	// node must NOT be excluded: vite, next and most JS dev servers are node.
	if infraCommands["node"] {
		t.Error("node is excluded — that discards exactly what this feature exists to find")
	}
}
