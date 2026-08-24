// Package loops implements recurring, autonomous agent workflows ("loops"): a loop watches a
// tracker for new tickets in a trigger category (e.g. "todo") and, for each new one, spawns an
// autonomous plan→execute session in a target repo — the ADE equivalent of Linear Loops. The engine
// owns config + dedup + run history; the actual session spawn is injected by the hub.
package loops

import (
	"context"
	"encoding/json"
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
}

// Issue is the slice of a tracker ticket the engine needs.
type Issue struct {
	Key, Title, Category, Provider string
}

// Engine owns loop config + run history and reacts to incoming issues.
type Engine struct {
	mu       sync.Mutex
	path     string
	loops    []Loop
	runs     []Run
	spawn    func(Loop, *Issue) (sessionID string, err error) // injected: starts the session (issue nil = task loop)
	onChange func()                                           // injected: notify clients config/runs changed
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
		active := e.activeRunCount(lp.ID)
		cap := lp.MaxConcurrent
		if cap < 1 {
			cap = 1
		}
		for _, iss := range issues {
			if active >= cap {
				break
			}
			if lp.TriggerCategory != "" && iss.Category != lp.TriggerCategory {
				continue
			}
			if lp.Tracker != "" && iss.Provider != lp.Tracker {
				continue
			}
			if e.isHandled(lp.ID, iss.Key) {
				continue
			}
			issCopy := iss
			sid, err := e.spawn(lp, &issCopy)
			e.markHandled(lp.ID, iss.Key)
			run := Run{LoopID: lp.ID, IssueKey: iss.Key, IssueTitle: iss.Title, SessionID: sid, StartedAt: e.now(), Status: "running"}
			if err != nil {
				run.Status = "error"
			}
			e.mu.Lock()
			e.runs = append(e.runs, run)
			if len(e.runs) > 200 { // bound history
				e.runs = e.runs[len(e.runs)-200:]
			}
			e.mu.Unlock()
			active++
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
		cap := lp.MaxConcurrent
		if cap < 1 {
			cap = 1
		}
		if e.activeRunCount(lp.ID) >= cap {
			continue // a prior run of this loop is still going — don't stack
		}
		sid, err := e.spawn(lp, nil) // nil issue = task loop → uses lp.Prompt
		e.setLastRun(lp.ID, now)
		run := Run{LoopID: lp.ID, IssueKey: "task", IssueTitle: lp.Name, SessionID: sid, StartedAt: now, Status: "running"}
		if err != nil {
			run.Status = "error"
		}
		e.appendRun(run)
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
	n := 0
	for _, r := range e.runs {
		if r.LoopID == loopID && r.Status == "running" {
			n++
		}
	}
	return n
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
