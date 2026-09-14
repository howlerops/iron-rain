package loops

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// MaxConcurrent must hold when two refreshes overlap.
//
// The cap was a check-then-act with the lock released across the whole spawn: the active run count
// was read once at the top of OnIssues and then incremented in a local variable, so two refreshes
// landing together both saw zero and each started up to the cap. A loop capped at one ran two agents
// on the same repo — which is exactly what the cap exists to prevent, because they share a worktree
// and undo each other.
//
// A spawn that takes real time is what makes the window visible; in production it takes seconds
// (a worktree, a process).
func TestOverlappingRefreshesRespectMaxConcurrent(t *testing.T) {
	var live, peak int64
	spawn := func(l Loop, iss *Issue) (string, error) {
		n := atomic.AddInt64(&live, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond) // a spawn is not instant
		atomic.AddInt64(&live, -1)
		return "sess_" + iss.Key, nil
	}

	e := New(filepath.Join(t.TempDir(), "loops.json"), spawn, func() {})
	e.Upsert(Loop{
		ID: "L1", Name: "watcher", Enabled: true, Kind: "ticket", Provider: "fake",
		ProjectIDs: []string{"p1"}, TriggerCategory: "todo", MaxConcurrent: 1,
	})

	var issues []Issue
	for i := 0; i < 8; i++ {
		issues = append(issues, Issue{Key: fmt.Sprintf("ENG-%d", i), Title: "t", Category: "todo", Provider: "linear"})
	}

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ { // four overlapping refreshes, as a tracker poll and a webhook would
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.OnIssues(issues)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got > 1 {
		t.Fatalf("%d agents ran at once on a loop capped at 1.\n\n"+
			"Overlapping refreshes each read the active count before either had recorded a run, so both "+
			"passed the gate. Two autonomous agents then work the same repo — sharing one worktree and "+
			"undoing each other — which is the specific thing MaxConcurrent exists to prevent.", got)
	}
}

// A failed spawn on a TASK loop must record why, and say so in the log.
//
// The ticket path recorded Run.Error and logged; the task path set Status "error" and dropped the
// reason. The row showed a red dot and the bare word "error", and its only action — Open — does
// nothing, because there is no session id to open.
func TestAFailedTaskRunRecordsItsReason(t *testing.T) {
	spawn := func(Loop, *Issue) (string, error) {
		return "", fmt.Errorf("provider binary not found on PATH")
	}
	e := New(filepath.Join(t.TempDir(), "loops.json"), spawn, func() {})
	e.Upsert(Loop{
		ID: "T1", Name: "nightly sweep", Enabled: true, Kind: "task", Provider: "fake",
		ProjectIDs: []string{"p1"}, Prompt: "look for bugs", IntervalMinutes: 1,
	})

	e.runScheduled()

	runs := e.Runs()
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %d", len(runs))
	}
	if runs[0].Status != "error" {
		t.Fatalf("a failed spawn produced status %q", runs[0].Status)
	}
	if runs[0].Error == "" {
		t.Fatal("the run recorded no reason.\n\n" +
			"All the user sees is a red dot and the word \"error\", on a row whose only action opens a " +
			"session that does not exist. Nothing was logged either, so the daemon log cannot answer it.")
	}
}
