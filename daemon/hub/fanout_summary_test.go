package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// summarySess is a session that exists only to be a member of a fan-out group.
type summarySess struct{ ch chan agent.Event }

func (s *summarySess) ID() string                                    { return "v0" }
func (s *summarySess) Provider() string                              { return "fake" }
func (s *summarySess) Events() <-chan agent.Event                    { return s.ch }
func (s *summarySess) Prompt(context.Context, string) error          { return nil }
func (s *summarySess) Respond(context.Context, string, string) error { return nil }
func (s *summarySess) Stop(context.Context) error                    { return nil }
func (s *summarySess) Close() error                                  { return nil }

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

// TestFanoutCapConstant documents the bound a divided fan-out relies on: every variant is a real
// worktree plus a real agent process, so an arbitrary-length subtask list must not be honored.
func TestFanoutCapConstant(t *testing.T) {
	if maxFanoutVariants < 6 {
		t.Fatalf("the cap (%d) must not be below the race maximum of 6", maxFanoutVariants)
	}
	if maxFanoutVariants > 24 {
		t.Fatalf("the cap (%d) is high enough to exhaust the machine", maxFanoutVariants)
	}
}

// The judge spec must survive the first comparison.
//
// It was deleted as the summary was broadcast, on the theory that a judge runs once. But a group
// outlives one comparison: synthesizeFanout adds a variant and re-arms the done-latch (see
// TestSynthesisReArmsTheDoneLatch), so the summary is broadcast a second time for the same group —
// and by then the spec was gone. The synthesis round, which compares the merged result against the
// originals and is the one a judgement is most useful for, therefore never got a judge at all.
//
// The spec is per-GROUP and its lifetime is the group's: fanout.resolve and forgetFanoutIfEmpty are
// the two ways a group ends, and both drop it.
func TestTheJudgeSpecSurvivesUntilTheGroupEnds(t *testing.T) {
	h := New()
	h.fanoutJudge["g1"] = fanoutJudgeSpec{provider: "fake", projectID: "p1"}
	// A member, or broadcastFanoutSummary returns at its empty-results guard and never reaches the
	// line under test — which is how the first version of this test passed with the defect intact.
	h.mu.Lock()
	m := newManagedSession(h, &summarySess{ch: make(chan agent.Event, 4)}, sessionMeta{fanoutGroup: "g1"})
	h.sessions["v0"] = m
	h.mu.Unlock()

	h.broadcastFanoutSummary("g1")

	h.mu.Lock()
	spec, second := h.fanoutJudge["g1"]
	h.mu.Unlock()
	if !second {
		t.Fatal("the judge spec was consumed by the first comparison.\n\n" +
			"The synthesis round re-arms the done-latch and broadcasts a second summary for the same " +
			"group; with the spec gone it gets no judge — and that round, which compares the merged " +
			"result against the originals, is the one a judgement is most useful for.")
	}
	if spec.provider != "fake" {
		t.Errorf("spec.provider = %q, want the recorded one", spec.provider)
	}

	// And the group ending still drops it, or the map grows for the life of the daemon. The group
	// ends when its last member is gone — which is exactly what forgetFanoutIfEmpty checks.
	h.mu.Lock()
	delete(h.sessions, "v0")
	h.mu.Unlock()
	h.forgetFanoutIfEmpty("g1")
	h.mu.Lock()
	_, afterEnd := h.fanoutJudge["g1"]
	h.mu.Unlock()
	if afterEnd {
		t.Error("the spec outlived its group — this map would grow with every fan-out the daemon runs")
	}
}
