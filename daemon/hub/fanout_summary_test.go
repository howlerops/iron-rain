package hub

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestDiffStatCountsUncommittedWork: an agent that did the work but didn't commit still shows a
// change — measuring only committed work would report "0 files" for most variants.
func TestDiffStatCountsUncommittedWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "user.email=t@x", "-c", "user.name=t", "commit", "-qm", "base")
	base, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	baseRef := string(base[:len(base)-1])

	// Uncommitted edit — exactly what an agent leaves behind mid-review.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, ins, del := diffStat(dir, baseRef)
	if files != 1 || ins != 2 || del != 0 {
		t.Fatalf("diffStat = %d files, +%d, -%d; want 1 file, +2, -0", files, ins, del)
	}

	// A clean tree reports nothing rather than erroring.
	run("checkout", "--", "a.txt")
	if f, i, d := diffStat(dir, baseRef); f != 0 || i != 0 || d != 0 {
		t.Errorf("clean tree should report no change, got %d/%d/%d", f, i, d)
	}
	// A bogus path must not panic or report phantom changes.
	if f, _, _ := diffStat(filepath.Join(dir, "nope"), baseRef); f != 0 {
		t.Error("a missing worktree should report no change")
	}
}

// TestFanoutResultOrdering: successes first, then biggest change — a reviewer should see the
// substantive attempts at the top, not map-iteration order.
func TestFanoutResultOrdering(t *testing.T) {
	rs := []protocol.FanoutVariantResult{
		{Variant: 0, FilesChanged: 1},
		{Variant: 1, Failed: true, FilesChanged: 99},
		{Variant: 2, FilesChanged: 7},
		{Variant: 3, FilesChanged: 7},
	}
	sortFanoutResults(rs)
	got := []int{rs[0].Variant, rs[1].Variant, rs[2].Variant, rs[3].Variant}
	want := []int{2, 3, 0, 1} // 7-file variants (tie → variant order), then 1-file, then the failure
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
