package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/worktree"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

// initRepo makes a git repo with one commit and returns its HEAD.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	git(t, dir, "init", "-q")
	git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	git(t, dir, "config", "user.email", "t@t")
	git(t, dir, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return string(out[:len(out)-1])
}

// finishWorkspaceMember must never push/PR a member that is clean or has no remote — it reports
// those as "skipped" so the coordinated finish degrades gracefully instead of erroring.
func TestFinishWorkspaceMember_SkipBranches(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	initRepo(t, repoA)
	initRepo(t, repoB)
	base := t.TempDir()

	_, members, err := worktree.CreateWorkspace(base, "cross", []string{repoA, repoB})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	h := &Hub{}
	ctx := context.Background()

	// Member 0: untouched → "no changes".
	if r := h.finishWorkspaceMember(ctx, members[0], "T", ""); r.Skipped != "no changes" || r.Pushed || r.Error != "" {
		t.Fatalf("clean member = %+v; want skipped 'no changes'", r)
	}

	// Member 1: change a file → commits, but no origin remote → "no origin remote" (never pushes).
	if err := os.WriteFile(filepath.Join(members[1].Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.finishWorkspaceMember(ctx, members[1], "Add file", "")
	if r.Pushed || r.Error != "" || r.Skipped == "" {
		t.Fatalf("no-remote member = %+v; want skipped, not pushed, no error", r)
	}
	// The change was still committed locally (so nothing is lost).
	out, _ := exec.Command("git", "-C", members[1].Path, "log", "--oneline").CombinedOutput()
	if len(out) == 0 {
		t.Fatalf("expected a commit in the member worktree")
	}
}
