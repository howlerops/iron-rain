package hub

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
)

// refusingProvider starts nothing — a bad API key, a missing binary, a harness that will not run.
type refusingProvider struct{ name string }

func (p *refusingProvider) Name() string                                     { return p.name }
func (p *refusingProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *refusingProvider) Create(context.Context, string, string) (agent.Session, error) {
	return nil, errors.New("invalid api key")
}

// A create that fails after making a worktree must not leave the worktree behind.
//
// startSession creates the checkout and reserves a port, and only the bootstrap failure undid them.
// A provider that refuses to start — the common case, since a wrong API key is discovered exactly
// here — returned straight out, leaving a full checkout on disk and a port marked in use.
//
// These leaks are unreachable by every sweep the daemon has: SweepOrphans works from session
// records, and no record was ever written. So they accumulate silently, one per retry, and the user
// retries a failing key more than once.
func TestACreateThatFailsDoesNotLeaveItsWorktreeBehind(t *testing.T) {
	repo := leakGitRepo(t)

	reg, err := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := reg.Add(repo)
	if err != nil {
		t.Fatal(err)
	}

	h := New()
	h.Register(&refusingProvider{name: "refuser"})
	h.SetProjects(reg)

	before := countWorktrees(t, repo)
	_, err = h.startSession(context.Background(), protocol.SessionCreate{
		Provider: "refuser", ProjectID: proj.ID, Worktree: true,
	}, sessionMeta{}, nil)
	if err == nil {
		t.Fatal("the provider refused to start but startSession reported success")
	}

	if after := countWorktrees(t, repo); after != before {
		t.Errorf("the failed create left %d worktree(s) behind (%d before, %d after).\n\n"+
			"No session record was written, so nothing owns this checkout and no sweep can find it — "+
			"SweepOrphans works from the session records. Every retry of a bad API key leaks another one.",
			after-before, before, after)
	}
}

// countWorktrees counts the checkouts git knows about, the main one included.
func countWorktrees(t *testing.T, repo string) int {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	n := 0
	for _, line := range splitLines(string(out)) {
		if len(line) > 9 && line[:9] == "worktree " {
			n++
		}
	}
	return n
}

// leakGitRepo makes a real git repo with one commit, the minimum `git worktree add` accepts. The
// hub_test package has its own gitRepo; this test needs the unexported startSession, so it lives in
// package hub and brings its own.
func leakGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "first")
	return dir
}
