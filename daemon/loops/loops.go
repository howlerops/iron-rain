// Package loops implements recurring, autonomous agent workflows ("loops"): a loop watches a
// tracker for new tickets in a trigger category (e.g. "todo") and, for each new one, spawns an
// autonomous plan→execute session in a target repo — the ADE equivalent of Linear Loops. The engine
// owns config + dedup + run history; the actual session spawn is injected by the hub.
package loops

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// Loop is a user-configured recurring workflow. Two kinds:
//   - "ticket": watch a tracker for new tickets in a category and start an agent on each.
//   - "task":   run a custom prompt (e.g. "scan for bugs, file issues, fix them" or "review open
//     PRs") on a schedule — the agent uses its MCP tools + repo access to do the job.
type Loop struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"` // agent to run (opencode/claude-code/…)
	Kind     string `json:"kind"`     // "ticket" (default) | "task"

	ProjectID  string   `json:"project_id,omitempty"`  // legacy single repo (migrated to ProjectIDs)
	ProjectIDs []string `json:"project_ids,omitempty"` // one or more repos (multi-root workspace)

	// ticket kind:
	TriggerCategory string `json:"trigger_category,omitempty"` // category that fires a run (default "todo")
	Tracker         string `json:"tracker,omitempty"`          // only this tracker (linear/jira); "" = any

	// task kind:
	Prompt          string `json:"prompt,omitempty"`           // the recurring job the agent performs
	IntervalMinutes int    `json:"interval_minutes,omitempty"` // schedule between runs (default 360 = 6h)
	LastRun         int64  `json:"last_run,omitempty"`         // unix seconds of the last scheduled run

	Worktree      bool     `json:"worktree"`
	Plan          bool     `json:"plan"`
	BudgetUSD     float64  `json:"budget_usd"`
	MaxConcurrent int      `json:"max_concurrent"`
	Handled       []string `json:"handled,omitempty"` // ticket keys already started (dedup, ticket kind)
}

// Repos returns the loop's target repos, migrating the legacy single ProjectID.
func (l Loop) Repos() []string {
	if len(l.ProjectIDs) > 0 {
		return l.ProjectIDs
	}
	if l.ProjectID != "" {
		return []string{l.ProjectID}
	}
	return nil
}

// Run is one loop execution — a ticket that got an autonomous session.
type Run struct {
	LoopID     string `json:"loop_id"`
	IssueKey   string `json:"issue_key"`
	IssueTitle string `json:"issue_title"`
	SessionID  string `json:"session_id"`
	Status     string `json:"status"` // running | done | error
	StartedAt  int64  `json:"started_at"`
	// Error is why a run failed to start or ended badly. It had nowhere to live: spawn's error was
	// used to set Status and then dropped, so a loop that could not start a session showed a red dot
	// and the bare word "error" with no reason, on a row whose Open button does nothing because
	// there is no session id. Nothing was logged either, so not even the daemon log explained it.
	Error string `json:"error,omitempty"`

	// seq identifies this run inside the process, so the placeholder recorded when a slot is reserved
	// can be found again once the spawn returns. Unexported, and therefore not persisted: it means
	// nothing across a restart, and a restored run is never mid-spawn.
	seq uint64
}

// Issue is the slice of a tracker ticket the engine needs.
type Issue struct {
	Key, Title, Category, Provider string
}

// Engine owns loop config + run history and reacts to incoming issues.
type Engine struct {
	mu    sync.Mutex
	path  string
	loops []Loop
	runs  []Run
	spawn func(Loop, *Issue) (sessionID string, err error) // injected: starts the session (issue nil = task loop)
	// runSeq numbers reserved runs so a placeholder can be found again once its spawn returns.
	runSeq   uint64
	onChange func() // injected: notify clients config/runs changed
	now      func() int64
}

type persisted struct {
	Loops []Loop `json:"loops"`
	Runs  []Run  `json:"runs"`
}

// New loads the engine from path. spawn starts a session for a (loop, issue); onChange notifies clients.
func New(path string, spawn func(Loop, *Issue) (string, error), onChange func()) *Engine {
	e := &Engine{path: path, spawn: spawn, onChange: onChange, now: func() int64 { return time.Now().Unix() }}
	if data, err := os.ReadFile(path); err == nil {
		var p persisted
		if json.Unmarshal(data, &p) == nil {
			e.loops = p.Loops
			e.runs = p.Runs
		}
	}
	// A run cannot still be running: this process has just started, so whatever session it named is
	// gone. Nothing reconciled this, and the runs were loaded verbatim — so a daemon that was killed
	// mid-run (which is every reboot, since oculusd is an app-child) came back holding a "running"
	// row forever. With MaxConcurrent defaulting to 1 that permanently wedges the loop: every tick
	// hits the concurrency gate and continues, silently, while the UI still shows the loop enabled
	// with a live run.
	interrupted := 0
	for i := range e.runs {
		if e.runs[i].Status == "running" {
			e.runs[i].Status = "error"
			interrupted++
		}
	}
	if interrupted > 0 {
		log.Printf("loops: %d run(s) were still marked running from a previous daemon — retiring them, "+
			"or their loops would never fire again", interrupted)
		e.persist()
	}
	return e
}

// List returns a copy of the configured loops.
func (e *Engine) List() []Loop {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Loop(nil), e.loops...)
}

// Runs returns a copy of the run history (most recent first).
func (e *Engine) Runs() []Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]Run(nil), e.runs...)
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Upsert creates or replaces a loop by ID, returning the stored value.
func (e *Engine) Upsert(l Loop) Loop {
	e.mu.Lock()
	if l.Kind == "" {
		l.Kind = "ticket"
	}
	if l.TriggerCategory == "" {
		l.TriggerCategory = "todo"
	}
	if l.Kind == "task" && l.IntervalMinutes <= 0 {
		l.IntervalMinutes = 360 // 6h default
	}
	if l.MaxConcurrent < 1 {
		l.MaxConcurrent = 1
	}
	replaced := false
	for i := range e.loops {
		if e.loops[i].ID == l.ID {
			l.Handled = e.loops[i].Handled // preserve dedup state across edits
			// LastRun too. The wire type carries it out (toProtoLoops) but loop.upsert builds its
			// engine value without it, so every edit reset the schedule clock to zero — and
			// runScheduled's gate is `LastRun != 0 && now-LastRun < interval`. Renaming a 6-hour
			// loop, or tweaking its prompt, therefore launched an unrequested autonomous run
			// (worktree, budget, the lot) within the next minute, and restarted the interval from
			// the edit. Three adjustments in a row meant three runs. Deterministic, no race needed.
			if l.LastRun == 0 {
				l.LastRun = e.loops[i].LastRun
			}
			e.loops[i] = l
			replaced = true
			break
		}
	}
	if !replaced {
		e.loops = append(e.loops, l)
	}
	e.mu.Unlock()
	e.persist()
	e.notify()
	return l
}

// Delete removes a loop (and its runs).
func (e *Engine) Delete(id string) {
	e.mu.Lock()
	e.loops = filterLoops(e.loops, func(l Loop) bool { return l.ID != id })
	e.runs = filterRuns(e.runs, func(r Run) bool { return r.LoopID != id })
	e.mu.Unlock()
	e.persist()
	e.notify()
}

// SetEnabled toggles a loop on/off.
func (e *Engine) SetEnabled(id string, on bool) {
	e.mu.Lock()
	for i := range e.loops {
		if e.loops[i].ID == id {
			e.loops[i].Enabled = on
		}
	}
	e.mu.Unlock()
	e.persist()
	e.notify()
}

// OnIssues is called whenever the tracker issue set refreshes. For each enabled loop it starts a run
// for every new matching ticket, up to the loop's concurrency cap.
func (e *Engine) OnIssues(issues []Issue) {
	e.mu.Lock()
	loops := append([]Loop(nil), e.loops...)
	e.mu.Unlock()

	changed := false
	for _, lp := range loops {
		if !lp.Enabled || lp.Kind == "task" || len(lp.Repos()) == 0 {
			continue // task loops run on a schedule (see runScheduled), not on ticket arrival
		}
		for _, iss := range issues {
			if lp.TriggerCategory != "" && iss.Category != lp.TriggerCategory {
				continue
			}
			if lp.Tracker != "" && iss.Provider != lp.Tracker {
				continue
			}
			// CLAIM the ticket and a concurrency slot together, before spawning. Separately, with the
			// lock released in between, two overlapping refreshes both passed the cap. See reserve.
			seq, ok := e.reserve(lp.ID, iss.Key, iss.Title, lp.MaxConcurrent, e.now())
			if !ok {
				continue // at the cap, or this ticket is already handled
			}
			issCopy := iss
			sid, err := e.spawn(lp, &issCopy)
			e.finish(seq, lp.ID, iss.Key, lp.Name, sid, err)
			changed = true
		}
	}
	if changed {
		e.persist()
		e.notify()
	}
}

// StartScheduler drives task-kind loops on their interval. Call once at startup; tick is how often the
// engine re-checks (e.g. 1 minute) — each task loop fires when IntervalMinutes has elapsed since its
// last run. Task loops run a custom prompt (using the agent's MCP tools) across the loop's repos.
func (e *Engine) StartScheduler(ctx context.Context, tick time.Duration) {
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.runScheduled()
			}
		}
	}()
}

func (e *Engine) runScheduled() {
	e.mu.Lock()
	loops := append([]Loop(nil), e.loops...)
	e.mu.Unlock()

	now := e.now()
	changed := false
	for _, lp := range loops {
		if !lp.Enabled || lp.Kind != "task" || lp.Prompt == "" || len(lp.Repos()) == 0 {
			continue
		}
		interval := int64(lp.IntervalMinutes) * 60
		if interval <= 0 {
			interval = 6 * 3600
		}
		if lp.LastRun != 0 && now-lp.LastRun < interval {
			continue // not due yet
		}
		// Reserve the slot before spawning, for the same reason OnIssues does: a spawn takes seconds,
		// and until its run is recorded the active count cannot see it, so two ticks landing together
		// both stacked a run on a loop capped at one.
		seq, ok := e.reserve(lp.ID, "task", lp.Name, lp.MaxConcurrent, now)
		if !ok {
			continue // a prior run of this loop is still going — don't stack
		}
		sid, err := e.spawn(lp, nil) // nil issue = task loop → uses lp.Prompt
		e.setLastRun(lp.ID, now)
		e.finish(seq, lp.ID, "task", lp.Name, sid, err)
		changed = true
	}
	if changed {
		e.persist()
		e.notify()
	}
}

func (e *Engine) setLastRun(loopID string, ts int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.loops {
		if e.loops[i].ID == loopID {
			e.loops[i].LastRun = ts
		}
	}
}

func (e *Engine) appendRun(r Run) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runs = append(e.runs, r)
	if len(e.runs) > 200 {
		e.runs = e.runs[len(e.runs)-200:]
	}
}

// SetRunStatus updates a run's status when its session ends (idle/done → "done", error → "error").
func (e *Engine) SetRunStatus(sessionID, status string) {
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	changed := false
	for i := range e.runs {
		if e.runs[i].SessionID == sessionID && e.runs[i].Status != status {
			e.runs[i].Status = status
			changed = true
		}
	}
	e.mu.Unlock()
	if changed {
		e.persist()
		e.notify()
	}
}

// --- internals ---

func (e *Engine) activeRunCount(loopID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeRunCountLocked(loopID)
}

func (e *Engine) activeRunCountLocked(loopID string) int {
	n := 0
	for _, r := range e.runs {
		if r.LoopID == loopID && r.Status == "running" {
			n++
		}
	}
	return n
}

// reserve takes a concurrency slot (and, for a ticket loop, the ticket itself) and records the run
// that will fill it — all under ONE lock.
//
// MaxConcurrent used to be a check-then-act with the lock released across the whole spawn: the
// active count was read once at the top of OnIssues and then incremented in a local variable, so two
// overlapping refreshes both saw zero and each started up to the cap. A loop capped at one ran two
// agents on the same repo, which is the specific thing the cap exists to prevent — two agents
// editing one worktree, each undoing the other.
//
// The placeholder run is what makes the slot real: a spawn takes seconds, and until its run is
// recorded the count cannot see it. Its seq is returned so finish() can fill in the session id, or
// mark it failed, once the spawn returns.
func (e *Engine) reserve(loopID, issueKey, issueTitle string, maxConcurrent int, now int64) (uint64, bool) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeRunCountLocked(loopID) >= maxConcurrent {
		return 0, false
	}
	// A ticket loop also claims the ticket here; a task loop (issueKey "task") has none to claim.
	if issueKey != "" && issueKey != "task" {
		if !e.claimLocked(loopID, issueKey) {
			return 0, false
		}
	}
	e.runSeq++
	seq := e.runSeq
	e.runs = append(e.runs, Run{
		LoopID: loopID, IssueKey: issueKey, IssueTitle: issueTitle,
		StartedAt: now, Status: "running", seq: seq,
	})
	if len(e.runs) > 200 { // bound history
		e.runs = e.runs[len(e.runs)-200:]
	}
	return seq, true
}

// finish completes the run reserved under seq: it records the session the spawn produced, or the
// reason there is none.
func (e *Engine) finish(seq uint64, loopID, issueKey, loopName, sid string, err error) {
	e.mu.Lock()
	for i := range e.runs {
		if e.runs[i].seq != seq {
			continue
		}
		e.runs[i].SessionID = sid
		if err != nil {
			e.runs[i].Status = "error"
			// Both of these were missing on the task-loop path: Status was set to "error" and the
			// reason dropped, so the row showed a red dot and the bare word "error", with an Open
			// button that does nothing because there is no session id. Nothing was logged either, so
			// not even the daemon log explained it.
			e.runs[i].Error = err.Error()
		}
		break
	}
	e.mu.Unlock()
	if err == nil {
		return
	}
	if issueKey != "" && issueKey != "task" {
		// Hand the ticket back. A spawn that failed for a transient, fixable reason — the provider
		// binary missing, a worktree that could not be created, a project path that moved — must not
		// blacklist it permanently.
		e.release(loopID, issueKey)
		log.Printf("loops: %q could not start a session for %s: %v — leaving the ticket unclaimed so "+
			"the next tick can retry it", loopName, issueKey, err)
		return
	}
	log.Printf("loops: %q could not start its scheduled run: %v", loopName, err)
}

func (e *Engine) isHandled(loopID, key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.loops {
		if e.loops[i].ID == loopID {
			for _, h := range e.loops[i].Handled {
				if h == key {
					return true
				}
			}
		}
	}
	return false
}

// claim atomically reserves a ticket for a loop, returning false when it was already taken.
//
// isHandled + spawn + markHandled was a check-then-act with the engine lock RELEASED for the whole
// spawn — which creates a worktree and a provider session, so the window is seconds wide. Manager
// .Refresh runs its update callback outside its own lock and is fired from the 60s poll, the startup
// fetch and three client-triggered handlers, each on its own goroutine; two of them overlapping
// meant both saw the ticket unclaimed and both called spawn. Two autonomous sessions on one ticket,
// in two worktrees, both spending budget.
func (e *Engine) claim(loopID, key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.claimLocked(loopID, key)
}

// claimLocked is claim with e.mu already held, so a caller can make the claim and the concurrency
// check one atomic step. See reserve.
func (e *Engine) claimLocked(loopID, key string) bool {
	for i := range e.loops {
		if e.loops[i].ID != loopID {
			continue
		}
		for _, h := range e.loops[i].Handled {
			if h == key {
				return false
			}
		}
		e.loops[i].Handled = append(e.loops[i].Handled, key)
		if len(e.loops[i].Handled) > 500 { // bound
			e.loops[i].Handled = e.loops[i].Handled[len(e.loops[i].Handled)-500:]
		}
		return true
	}
	return false // no such loop
}

// release gives a claimed ticket back, for a spawn that failed. Without it a transient failure —
// a missing provider binary, a worktree that could not be created — would blacklist the ticket
// permanently, and the loop would never retry it even after the cause was fixed.
func (e *Engine) release(loopID, key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.loops {
		if e.loops[i].ID != loopID {
			continue
		}
		kept := e.loops[i].Handled[:0]
		for _, h := range e.loops[i].Handled {
			if h != key {
				kept = append(kept, h)
			}
		}
		e.loops[i].Handled = kept
		return
	}
}

func (e *Engine) markHandled(loopID, key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.loops {
		if e.loops[i].ID == loopID {
			e.loops[i].Handled = append(e.loops[i].Handled, key)
			if len(e.loops[i].Handled) > 500 { // bound
				e.loops[i].Handled = e.loops[i].Handled[len(e.loops[i].Handled)-500:]
			}
		}
	}
}

func (e *Engine) notify() {
	if e.onChange != nil {
		e.onChange()
	}
}

func (e *Engine) persist() {
	if e.path == "" {
		return
	}
	e.mu.Lock()
	p := persisted{Loops: append([]Loop(nil), e.loops...), Runs: append([]Run(nil), e.runs...)}
	e.mu.Unlock()
	if data, err := json.MarshalIndent(p, "", "  "); err == nil {
		_ = os.WriteFile(e.path, data, 0o600)
	}
}

func filterLoops(in []Loop, keep func(Loop) bool) []Loop {
	out := in[:0]
	for _, l := range in {
		if keep(l) {
			out = append(out, l)
		}
	}
	return append([]Loop(nil), out...)
}

func filterRuns(in []Run, keep func(Run) bool) []Run {
	out := in[:0]
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		}
	}
	return append([]Run(nil), out...)
}
