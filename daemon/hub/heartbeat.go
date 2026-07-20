package hub

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Heartbeat supervision keeps long-running autonomous sessions on track. Every tick it derives
// one state per session from signals the event pump already records (status, todos, activity,
// cost) and — for sessions the user opted into (Autonomous) — nudges an idle-but-unfinished
// agent to continue, checkpoints its context to a durable handoff file before it compacts, and
// escalates to a push when it's stuck or has exhausted its nudge/cost budget. Non-autonomous
// (e.g. plain chat) sessions are observed but never touched.
const (
	heartbeatInterval = 25 * time.Second
	activeWindow      = 45 * time.Second // recent activity → still WORKING
	idleGrace         = 60 * time.Second // idle must persist this long before a nudge
	stallWindow       = 5 * time.Minute  // idle+incomplete this long with no activity → STALLED
	awaitWindow       = 2 * time.Minute  // unanswered approval this long → remind once
	defaultMaxNudges  = 6                // ralph-style give-up bound
	defaultBudgetUSD  = 5.0              // cost ceiling for auto-nudging
	checkpointTokens  = 120_000          // tokens since last handoff checkpoint → ask it to save state
)

// Heartbeat states (derived, not stored by providers).
const (
	hbWorking        = "working"
	hbAwaitingInput  = "awaiting_input"
	hbIdleIncomplete = "idle_incomplete"
	hbStalled        = "stalled"
	hbDone           = "done"
	hbErrored        = "errored"
	hbExhausted      = "exhausted"
)

// StartHeartbeat launches the supervision ticker (runs until ctx is cancelled). Call once at
// startup, alongside StartSessionPruning.
func (h *Hub) StartHeartbeat(ctx context.Context) {
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.heartbeatTick()
			}
		}
	}()
}

func (h *Hub) heartbeatTick() {
	h.mu.Lock()
	sessions := make([]*managedSession, 0, len(h.sessions))
	for _, m := range h.sessions {
		sessions = append(sessions, m)
	}
	h.mu.Unlock()

	now := time.Now()
	for _, m := range sessions {
		m.mu.Lock()
		st := deriveState(m, now)
		changed := st != m.hbState
		m.hbState = st
		auto := m.autonomous
		nudgeN := m.nudgeCount
		maxN := m.maxNudges
		if maxN == 0 {
			maxN = defaultMaxNudges
		}
		budget := m.budgetUSD
		if budget == 0 {
			budget = defaultBudgetUSD
		}
		cost := m.costUSD
		tokens := m.inTok + m.outTok
		lastCk := m.lastCheckpoint
		todos := append([]protocol.Todo(nil), m.latestTodos...)
		label := m.meta.label
		if label == "" {
			label = m.meta.workspaceName
		}
		handoff := handoffPath(m.meta.cwd, m.sess.ID())
		m.mu.Unlock()

		done, total := todoProgress(todos)
		if changed {
			h.broadcast(protocol.TypeSessionHeartbeat, protocol.SessionHeartbeat{
				SessionID: m.sess.ID(), State: st, NudgeCount: nudgeN,
				TodosDone: done, TodosTotal: total, CostUSD: cost, BudgetUSD: budget,
			})
		}

		if !auto {
			continue // opt-in supervision — never nudge a session the user didn't enroll
		}

		switch st {
		case hbIdleIncomplete:
			switch {
			case nudgeN >= maxN || cost >= budget:
				m.mu.Lock()
				m.autonomous = false
				m.hbState = hbExhausted
				m.mu.Unlock()
				h.broadcastHeartbeat(m, hbExhausted, nudgeN, done, total, cost, budget)
				h.pushAgentStalled(m.sess.ID(), label,
					fmt.Sprintf("used %d nudges / $%.2f — needs you", nudgeN, cost))
			case tokens-lastCk >= checkpointTokens:
				// Context is getting large: ask the agent to externalize state before it
				// compacts, then let it continue next tick. Not counted against the nudge cap.
				m.mu.Lock()
				m.lastCheckpoint = tokens
				m.lastNudge = now
				m.mu.Unlock()
				_ = m.sess.Prompt(context.Background(), checkpointNudge(handoff))
			default:
				m.mu.Lock()
				m.nudgeCount++
				m.lastNudge = now
				m.mu.Unlock()
				_ = m.sess.Prompt(context.Background(), continueNudge(todos, handoff))
				log.Printf("heartbeat: nudged %s (%d/%d)", m.sess.ID(), nudgeN+1, maxN)
			}
		case hbStalled:
			if changed {
				h.pushAgentStalled(m.sess.ID(), label, "stalled with unfinished work")
			}
		case hbAwaitingInput:
			// Fresh approvals already pushed on arrival; remind once if it's gone stale.
			m.mu.Lock()
			stale := now.Sub(m.lastActivity) > awaitWindow
			m.mu.Unlock()
			if stale && changed {
				h.pushAgentStalled(m.sess.ID(), label, "waiting for your approval")
			}
		}
	}
}

func (h *Hub) broadcastHeartbeat(m *managedSession, state string, nudge, done, total int, cost, budget float64) {
	h.broadcast(protocol.TypeSessionHeartbeat, protocol.SessionHeartbeat{
		SessionID: m.sess.ID(), State: state, NudgeCount: nudge,
		TodosDone: done, TodosTotal: total, CostUSD: cost, BudgetUSD: budget,
	})
}

// deriveState computes a session's supervision state. Caller holds m.mu.
func deriveState(m *managedSession, now time.Time) string {
	if m.lastStatus == protocol.StatusError {
		return hbErrored
	}
	if m.pendingApprovals > 0 || m.lastStatus == protocol.StatusAwaitingApproval {
		return hbAwaitingInput
	}
	if now.Sub(m.lastActivity) < activeWindow {
		return hbWorking
	}
	_, total := todoProgress(m.latestTodos)
	incomplete := false
	for _, t := range m.latestTodos {
		if t.Status != "completed" {
			incomplete = true
			break
		}
	}
	if total > 0 && !incomplete && m.turnEnded {
		return hbDone
	}
	maxN := m.maxNudges
	if maxN == 0 {
		maxN = defaultMaxNudges
	}
	budget := m.budgetUSD
	if budget == 0 {
		budget = defaultBudgetUSD
	}
	if m.nudgeCount >= maxN || m.costUSD >= budget {
		return hbExhausted
	}
	if incomplete && now.Sub(m.lastActivity) > stallWindow {
		return hbStalled
	}
	if incomplete && now.Sub(m.lastActivity) > idleGrace {
		return hbIdleIncomplete
	}
	return hbWorking // within the grace window — give it room
}

func todoProgress(todos []protocol.Todo) (done, total int) {
	for _, t := range todos {
		total++
		if t.Status == "completed" {
			done++
		}
	}
	return
}

// handoffPath is the durable per-session progress/handoff file (agent-authored, survives
// context compaction, forks, and restarts).
func handoffPath(cwd, sessionID string) string {
	if cwd == "" {
		return ""
	}
	return cwd + "/.oculus/handoff/" + sessionID + ".md"
}

func continueNudge(todos []protocol.Todo, handoff string) string {
	var next string
	for _, t := range todos {
		if t.Status == "in_progress" {
			next = t.Content
			break
		}
	}
	if next == "" {
		for _, t := range todos {
			if t.Status == "pending" {
				next = t.Content
				break
			}
		}
	}
	b := &strings.Builder{}
	b.WriteString("You still have unfinished work. Continue with your plan")
	if next != "" {
		b.WriteString(" — next: ")
		b.WriteString(next)
	}
	b.WriteString(". If a step is blocked and needs a human decision, say so explicitly and stop; otherwise keep going until every to-do is complete.")
	if handoff != "" {
		b.WriteString(" Keep " + handoff + " current as you go.")
	}
	return b.String()
}

func checkpointNudge(handoff string) string {
	p := handoff
	if p == "" {
		p = ".oculus/handoff/progress.md"
	}
	return "Before you continue, update the handoff file at " + p +
		" with: the objective, what's Done, what's In progress (with the next concrete step), key Decisions and why, and Key files. " +
		"Keep it concise — this is what survives if your context is summarized. Then continue your work."
}
