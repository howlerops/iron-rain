package loops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A run that was still "running" when the daemon died must not wedge its loop forever.
//
// New loaded runs from disk verbatim and nothing reconciled them. But a run cannot still be running
// in a process that has only just started — whatever session it named is gone. With MaxConcurrent
// defaulting to 1, one stale row means every later tick hits the concurrency gate and `continue`s,
// silently, so a recurring autonomous workflow fires exactly once and never again while every
// surface still shows it enabled with a live run. oculusd is an app-child by default, so this is
// what EVERY reboot produced.
func TestRunsLeftRunningByADeadDaemonAreRetiredOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loops.json")
	seed := persisted{
		Loops: []Loop{{ID: "l1", Name: "nightly", Enabled: true, ProjectIDs: []string{"p1"}}},
		Runs: []Run{
			{LoopID: "l1", IssueKey: "ENG-1", SessionID: "s1", Status: "running", StartedAt: 100},
			{LoopID: "l1", IssueKey: "ENG-2", SessionID: "s2", Status: "done", StartedAt: 90},
		},
	}
	b, _ := json.Marshal(seed)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	e := New(path, func(Loop, *Issue) (string, error) { return "", nil }, nil)
	for _, r := range e.Runs() {
		if r.Status == "running" {
			t.Errorf("run for %s came back as still running after a restart. Its session does not "+
				"exist, so nothing will ever retire it — and the loop's concurrency gate never "+
				"reopens, which means it never fires again.", r.IssueKey)
		}
	}
	// The reconciliation must be persisted, or it is redone (and re-logged) on every start.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back persisted
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, r := range back.Runs {
		if r.Status == "running" {
			t.Errorf("the retirement was not written back for %s", r.IssueKey)
		}
	}
}

// A ticket whose session failed to START must stay retryable.
//
// markHandled ran BEFORE the spawn error was examined, so a transient failure — a missing provider
// binary, a worktree that could not be created, a project path that had moved — blacklisted that
// ticket permanently. The loop would never pick it up again, not even after the cause was fixed, and
// the error itself was dropped: no field on Run, nothing logged, nothing sent. The Loops screen
// showed a red dot and the bare word "error" on a row whose Open button does nothing, because a run
// that never started has no session to open.
func TestATicketWhoseSpawnFailedIsRetriedAndSaysWhy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loops.json")
	fail := true
	spawns := 0
	e := New(path, func(Loop, *Issue) (string, error) {
		spawns++
		if fail {
			return "", errors.New("gemini: executable file not found in $PATH")
		}
		return "sess-ok", nil
	}, nil)
	e.Upsert(Loop{ID: "l1", Name: "tickets", Enabled: true, TriggerCategory: "todo", ProjectIDs: []string{"p1"}})

	issues := []Issue{{Key: "ENG-7", Title: "Fix login", Category: "todo"}}
	e.OnIssues(issues)
	if spawns != 1 {
		t.Fatalf("spawn ran %d times on the first pass, want 1", spawns)
	}
	runs := e.Runs()
	if len(runs) != 1 || runs[0].Status != "error" {
		t.Fatalf("runs = %+v, want one errored run", runs)
	}
	if runs[0].Error == "" {
		t.Error("the failure carried no reason. The user sees a red dot, the word \"error\", and a " +
			"button that does nothing — with no way to find out what went wrong, not even from the " +
			"daemon log.")
	}

	// The cause is fixed; the next tick must try again.
	fail = false
	e.OnIssues(issues)
	if spawns != 2 {
		t.Fatalf("the ticket was never retried after the failure was fixed (spawns=%d). markHandled "+
			"claimed it before the error was looked at, so this loop has silently abandoned a ticket "+
			"it will never touch again.", spawns)
	}
}

// Two concurrent tracker refreshes must not start two agents on one ticket.
//
// isHandled + spawn + markHandled was a check-then-act with the engine lock RELEASED for the entire
// spawn — which creates a worktree and a provider session, so the window is seconds wide.
// Manager.Refresh invokes its update callback outside its own lock and is fired from the 60s poll,
// the startup fetch and three client-triggered handlers, each on its own goroutine. Two overlapping
// meant both saw the ticket unclaimed and both spawned: two autonomous sessions on one ticket, in
// two worktrees, both spending budget.
func TestConcurrentRefreshesStartOneAgentPerTicket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loops.json")
	var mu sync.Mutex
	spawned := map[string]int{}
	e := New(path, func(_ Loop, iss *Issue) (string, error) {
		mu.Lock()
		spawned[iss.Key]++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // a worktree + a provider session take real time
		return "sess-" + iss.Key, nil
	}, nil)
	e.Upsert(Loop{ID: "l1", Name: "tickets", Enabled: true, TriggerCategory: "todo",
		ProjectIDs: []string{"p1"}, MaxConcurrent: 4})

	issues := []Issue{{Key: "ENG-7", Title: "Fix login", Category: "todo"}}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.OnIssues(issues) }()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if spawned["ENG-7"] != 1 {
		t.Errorf("started %d agents for one ticket.\n\nEach gets its own worktree and its own "+
			"budget, and whichever finishes badly then wedges the loop.", spawned["ENG-7"])
	}
}
