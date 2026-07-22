// Package loops implements recurring, autonomous agent workflows ("loops"): a loop watches a
// tracker for new tickets in a trigger category (e.g. "todo") and, for each new one, spawns an
// autonomous plan→execute session in a target repo — the ADE equivalent of Linear Loops. The engine
// owns config + dedup + run history; the actual session spawn is injected by the hub.
package loops

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Loop is a user-configured recurring workflow.
type Loop struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Enabled         bool    `json:"enabled"`
	Provider        string  `json:"provider"`          // agent to run (opencode/claude-code/…)
	ProjectID       string  `json:"project_id"`        // repo the tickets are built in
	TriggerCategory string  `json:"trigger_category"`  // ticket category that fires a run (default "todo")
	Tracker         string  `json:"tracker,omitempty"` // only this tracker (linear/jira); "" = any
	Worktree        bool    `json:"worktree"`          // run each ticket in its own git worktree
	Plan            bool    `json:"plan"`              // start in plan mode (propose a plan first)
	BudgetUSD       float64 `json:"budget_usd"`        // per-run cost ceiling for autonomous nudging
	MaxConcurrent   int     `json:"max_concurrent"`    // cap on simultaneous runs (default 1)
	Handled         []string `json:"handled,omitempty"` // issue keys already started (dedup)
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
	spawn    func(Loop, Issue) (sessionID string, err error) // injected: starts the autonomous session
	onChange func()                                           // injected: notify clients config/runs changed
	now      func() int64
}

type persisted struct {
	Loops []Loop `json:"loops"`
	Runs  []Run  `json:"runs"`
}

// New loads the engine from path. spawn starts a session for a (loop, issue); onChange notifies clients.
func New(path string, spawn func(Loop, Issue) (string, error), onChange func()) *Engine {
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
	if l.TriggerCategory == "" {
		l.TriggerCategory = "todo"
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
		if !lp.Enabled || lp.ProjectID == "" {
			continue
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
			sid, err := e.spawn(lp, iss)
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
