package loops

import (
	"path/filepath"
	"sync"
	"testing"
)

// newTestEngine builds an engine with a controllable clock and a spawn recorder.
func newTestEngine(t *testing.T) (*Engine, *spawnRec) {
	t.Helper()
	rec := &spawnRec{}
	e := New(filepath.Join(t.TempDir(), "loops.json"), rec.spawn, func() {})
	e.now = func() int64 { return rec.clock }
	return e, rec
}

type spawnRec struct {
	mu    sync.Mutex
	clock int64
	calls []spawnCall
}

type spawnCall struct {
	loop  Loop
	issue *Issue
}

func (s *spawnRec) spawn(l Loop, iss *Issue) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spawnCall{loop: l, issue: iss})
	return "sess_" + l.ID, nil
}

func (s *spawnRec) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }

func TestReposMigratesLegacyField(t *testing.T) {
	if got := (Loop{ProjectID: "p1"}).Repos(); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("legacy ProjectID not migrated: %v", got)
	}
	if got := (Loop{ProjectIDs: []string{"a", "b"}}).Repos(); len(got) != 2 {
		t.Fatalf("ProjectIDs not returned: %v", got)
	}
	if got := (Loop{}).Repos(); got != nil {
		t.Fatalf("empty loop should have no repos: %v", got)
	}
}

func TestUpsertDefaults(t *testing.T) {
	e, _ := newTestEngine(t)
	got := e.Upsert(Loop{ID: "l1", Kind: "task"})
	if got.IntervalMinutes != 360 {
		t.Fatalf("task interval default = %d, want 360", got.IntervalMinutes)
	}
	if got.MaxConcurrent != 1 {
		t.Fatalf("MaxConcurrent default = %d, want 1", got.MaxConcurrent)
	}
	def := e.Upsert(Loop{ID: "l2"})
	if def.Kind != "ticket" {
		t.Fatalf("default kind = %q, want ticket", def.Kind)
	}
}

func TestOnIssuesSkipsTaskLoops(t *testing.T) {
	e, rec := newTestEngine(t)
	e.Upsert(Loop{ID: "task1", Kind: "task", Enabled: true, ProjectIDs: []string{"p1"}, Prompt: "do it"})
	e.OnIssues([]Issue{{Key: "ENG-1", Category: "todo", Provider: "linear"}})
	if rec.count() != 0 {
		t.Fatalf("task loop should not fire on issues, got %d spawns", rec.count())
	}
}

func TestOnIssuesTicketLoopFiresOncePerTicket(t *testing.T) {
	e, rec := newTestEngine(t)
	e.Upsert(Loop{ID: "tk", Kind: "ticket", Enabled: true, ProjectIDs: []string{"p1"}, TriggerCategory: "todo", MaxConcurrent: 5})
	iss := []Issue{{Key: "ENG-1", Category: "todo", Provider: "linear"}}
	e.OnIssues(iss)
	e.OnIssues(iss) // second poll: already handled → no new spawn
	if rec.count() != 1 {
		t.Fatalf("ticket loop dedup failed: got %d spawns, want 1", rec.count())
	}
	if rec.calls[0].issue == nil || rec.calls[0].issue.Key != "ENG-1" {
		t.Fatalf("ticket spawn should carry the issue")
	}
}

func TestRunScheduledFiresWhenDueAndDedupsWhileActive(t *testing.T) {
	e, rec := newTestEngine(t)
	rec.clock = 1000
	e.Upsert(Loop{ID: "task1", Kind: "task", Enabled: true, ProjectIDs: []string{"p1"}, Prompt: "scan", IntervalMinutes: 10})

	e.runScheduled() // LastRun was 0 → due immediately
	if rec.count() != 1 {
		t.Fatalf("task loop should fire first time, got %d", rec.count())
	}
	if rec.calls[0].issue != nil {
		t.Fatalf("task spawn should pass a nil issue")
	}

	// A prior run is still "running" → don't stack even though interval elapsed.
	rec.clock = 1000 + 11*60
	e.runScheduled()
	if rec.count() != 1 {
		t.Fatalf("should not stack while active run exists, got %d", rec.count())
	}

	// Mark the run done; now a due tick should fire again.
	e.SetRunStatus("sess_task1", "done")
	rec.clock = 1000 + 22*60
	e.runScheduled()
	if rec.count() != 2 {
		t.Fatalf("task loop should fire again once idle+due, got %d", rec.count())
	}
}

func TestRunScheduledNotDue(t *testing.T) {
	e, rec := newTestEngine(t)
	rec.clock = 5000
	e.Upsert(Loop{ID: "task1", Kind: "task", Enabled: true, ProjectIDs: []string{"p1"}, Prompt: "scan", IntervalMinutes: 60})
	e.runScheduled() // fires (LastRun 0)
	e.SetRunStatus("sess_task1", "done")
	rec.clock = 5000 + 30*60 // only 30m of a 60m interval
	e.runScheduled()
	if rec.count() != 1 {
		t.Fatalf("task loop fired before interval elapsed, got %d", rec.count())
	}
}
