package loops

import "testing"

// Editing a loop must not restart its schedule.
//
// Upsert preserved Handled (the dedup set) but not LastRun, and loop.upsert in the hub builds its
// engine value straight off the wire without it — so every edit reset the schedule clock to zero.
// runScheduled's gate is `LastRun != 0 && now-LastRun < interval`, so renaming a 6-hour loop, or
// tweaking its prompt or budget, launched an unrequested autonomous run — worktree, agent, spend —
// within the next minute, and restarted the interval from the edit. Three adjustments in a row meant
// three runs. Deterministic; no race needed.
func TestEditingALoopKeepsItsSchedule(t *testing.T) {
	e, _ := newTestEngine(t)
	lp := e.Upsert(Loop{Name: "nightly", Prompt: "tidy up", IntervalMinutes: 360, Kind: "task"})

	const ranAt = int64(1_700_000_000)
	e.setLastRun(lp.ID, ranAt)

	// The user renames it. Nothing about the schedule was touched.
	edited := e.Upsert(Loop{ID: lp.ID, Name: "nightly tidy", Prompt: "tidy up",
		IntervalMinutes: 360, Kind: "task"})

	if edited.LastRun != ranAt {
		t.Fatalf("LastRun after an edit is %d, want %d.\n\n"+
			"With it zeroed the interval gate passes immediately, so saving a rename starts an "+
			"unrequested autonomous run within a minute and restarts the clock from the edit.",
			edited.LastRun, ranAt)
	}
}

// An explicit LastRun on the incoming value still wins — the engine sets it when a run actually
// starts, and that write must not be swallowed by the preservation above.
func TestAnExplicitLastRunIsNotOverwritten(t *testing.T) {
	e, _ := newTestEngine(t)
	lp := e.Upsert(Loop{Name: "nightly", Prompt: "tidy", IntervalMinutes: 360, Kind: "task"})
	e.setLastRun(lp.ID, 1000)

	updated := e.Upsert(Loop{ID: lp.ID, Name: "nightly", Prompt: "tidy",
		IntervalMinutes: 360, Kind: "task", LastRun: 2000})
	if updated.LastRun != 2000 {
		t.Fatalf("an explicit LastRun was overwritten with the stored one: got %d, want 2000",
			updated.LastRun)
	}
}
