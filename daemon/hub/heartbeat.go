package hub

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/transport"
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
		cwd := m.meta.cwd
		handoff := handoffPath(cwd, m.sess.ID())
		m.mu.Unlock()

		done, total := todoProgress(todos)
		if changed {
			h.broadcast(protocol.TypeSessionHeartbeat, protocol.SessionHeartbeat{
				SessionID: m.sess.ID(), State: st, NudgeCount: nudgeN,
				TodosDone: done, TodosTotal: total, CostUSD: cost, BudgetUSD: budget,
			})
		}

		// Index the agent-authored handoff file if it changed since last tick (cheap stat;
		// runs for every session so a hand-written handoff is picked up even without autonomy).
		h.indexHandoff(m, cwd, handoff)

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
				// Re-validate idle under the lock (the session may have resumed since the
				// snapshot) so we never inject a prompt into an in-flight turn.
				m.mu.Lock()
				stillIdle := deriveState(m, time.Now()) == hbIdleIncomplete
				if stillIdle {
					m.lastCheckpoint = tokens
					m.lastNudge = now
				}
				m.mu.Unlock()
				if stillIdle {
					_ = m.sess.Prompt(context.Background(), checkpointNudge(handoff))
				}
			default:
				m.mu.Lock()
				stillIdle := deriveState(m, time.Now()) == hbIdleIncomplete
				if stillIdle {
					m.nudgeCount++
					m.lastNudge = now
				}
				m.mu.Unlock()
				if stillIdle {
					_ = m.sess.Prompt(context.Background(), continueNudge(todos, handoff))
					log.Printf("heartbeat: nudged %s (%d/%d)", m.sess.ID(), nudgeN+1, maxN)
				}
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

// SendHeartbeatSnapshot pushes the CURRENT supervision state of every live session to ONE client.
// heartbeatTick broadcasts only on CHANGE, which is right for steady-state fan-out and wrong for a
// device that just arrived: pick up the phone while the Mac is mid-turn and it has missed every
// edge, so its chips stay blank (or worse, stay whatever the last session.list snapshot implied)
// until the state happens to flip — up to a full 25s tick. Call this when a client connects.
// Best-effort per frame: a client whose queue is full is skipped, never blocked on, exactly as
// broadcast does.
func (h *Hub) SendHeartbeatSnapshot(conn *transport.Conn) {
	h.mu.Lock()
	c := h.clients[conn]
	sessions := make([]*managedSession, 0, len(h.sessions))
	for _, m := range h.sessions {
		sessions = append(sessions, m)
	}
	h.mu.Unlock()
	if c == nil {
		return
	}
	now := time.Now()
	for _, m := range sessions {
		m.mu.Lock()
		st := m.hbState
		if st == "" {
			// No tick has run for this session yet (it was created since the last one) — derive it now
			// rather than sending an empty state the client would have to guess about.
			st = deriveState(m, now)
			m.hbState = st
		}
		nudge, cost := m.nudgeCount, m.costUSD
		budget := m.budgetUSD
		if budget == 0 {
			budget = defaultBudgetUSD
		}
		todos := append([]protocol.Todo(nil), m.latestTodos...)
		m.mu.Unlock()
		done, total := todoProgress(todos)
		raw, err := protocol.Encode("", protocol.TypeSessionHeartbeat, protocol.SessionHeartbeat{
			SessionID: m.sess.ID(), State: st, NudgeCount: nudge,
			TodosDone: done, TodosTotal: total, CostUSD: cost, BudgetUSD: budget,
		})
		if err != nil {
			continue
		}
		select {
		case c.ch <- raw:
		default:
		}
	}
}

func (h *Hub) broadcastHeartbeat(m *managedSession, state string, nudge, done, total int, cost, budget float64) {
	h.broadcast(protocol.TypeSessionHeartbeat, protocol.SessionHeartbeat{
		SessionID: m.sess.ID(), State: state, NudgeCount: nudge,
		TodosDone: done, TodosTotal: total, CostUSD: cost, BudgetUSD: budget,
	})
}

// indexHandoff stats the session's handoff file and, if it changed since the last index,
// parses a title + summary and upserts it into the store. Best-effort and silent: a missing
// file (the common case) or a store-less daemon is a no-op. Broadcasts handoff.list on change
// so connected clients refresh.
func (h *Hub) indexHandoff(m *managedSession, cwd, path string) {
	if h.db == nil || path == "" || cwd == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return
	}
	mtime := fi.ModTime().Unix()
	m.mu.Lock()
	unchanged := m.lastHandoffMtime == mtime
	if !unchanged {
		m.lastHandoffMtime = mtime
	}
	m.mu.Unlock()
	if unchanged {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	title, summary := parseHandoff(string(data))
	if err := h.db.UpsertHandoff(store.HandoffRecord{
		SessionID: m.sess.ID(), Cwd: cwd, Path: path,
		Title: title, Summary: summary, UpdatedAt: mtime,
	}); err != nil {
		return
	}
	if list, err := h.db.Handoffs(""); err == nil {
		h.broadcast(protocol.TypeHandoffList, protocol.HandoffList{Handoffs: toHandoffEntries(list)})
	}
}

// parseHandoff extracts a display title (first markdown heading or first non-empty line) and a
// short summary (the next few non-heading lines) from a handoff file.
func parseHandoff(md string) (title, summary string) {
	lines := strings.Split(md, "\n")
	var body []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if title == "" {
			title = stripMarkdownPrefix(t)
			continue
		}
		body = append(body, stripMarkdownPrefix(t))
		if len(body) >= 3 {
			break
		}
	}
	// Joined with a separator, not a bare space. These lines are usually LIST ITEMS, and running
	// them together produced summaries like "fixed the parser added tests updated docs" — three
	// facts read as one sentence, which is where the comparison cards looked garbled.
	summary = truncRunes(strings.Join(body, " · "), 240)
	return title, summary
}

// stripMarkdownPrefix removes a leading markdown marker — heading, bullet, quote — and nothing else.
//
// This used to be strings.TrimLeft(t, "#> -"), which takes a CHARACTER SET rather than a prefix, so
// it stripped every leading occurrence of any of those runes: "---" became "", "-3 items" became
// "3 items", and "## #1 priority" lost the "#1". Matching real prefixes keeps the text intact.
func stripMarkdownPrefix(s string) string {
	for _, p := range []string{"- [ ] ", "- [x] ", "* ", "- ", "> ", "###### ", "##### ", "#### ", "### ", "## ", "# "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(strings.TrimPrefix(s, p))
		}
	}
	return s
}

// truncRunes caps s at n BYTES without splitting a rune.
//
// A plain s[:n] slices BYTES, so a multi-byte character straddling the limit is cut in half and the
// invalid UTF-8 is rewritten to replacement characters when the event is encoded. Agent prose is
// full of em-dashes, curly quotes and emoji, so the boundary lands mid-rune often — this is where
// summaries picked up mojibake tails.
func truncRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func toHandoffEntries(rs []store.HandoffRecord) []protocol.HandoffEntry {
	out := make([]protocol.HandoffEntry, 0, len(rs))
	for _, r := range rs {
		out = append(out, protocol.HandoffEntry{
			SessionID: r.SessionID, Cwd: r.Cwd, Path: r.Path,
			Title: r.Title, Summary: r.Summary, UpdatedAt: r.UpdatedAt,
		})
	}
	return out
}

// deriveState computes a session's supervision state. Caller holds m.mu.
func deriveState(m *managedSession, now time.Time) string {
	if m.lastStatus == protocol.StatusError {
		return hbErrored
	}
	// A turn is OPEN: the turn engine owns this session's liveness, so stay out of it.
	//
	// These are two independent supervisors that could not see each other. This one judges from
	// lastActivity alone, so a running turn that went quiet for a minute — an agent mid-test-run —
	// was classified idle-and-incomplete and sent a "continue with your plan" prompt into a
	// conversation that was already working. The turn engine has far better evidence for the same
	// question (provider probes, tool progress, per-child clocks) and its own gentler ladder.
	//
	// Once the turn engine has itself declared the turn stalled, it has already spent that ladder,
	// so this supervisor is free to act again.
	if m.turnPhase != "" && m.turnPhase != protocol.StatusStalled {
		return hbWorking
	}
	if m.pendingApprovals > 0 || m.lastStatus == protocol.StatusAwaitingApproval {
		return hbAwaitingInput
	}
	if now.Sub(m.lastActivity) < activeWindow {
		return hbWorking
	}
	incomplete := false
	for _, t := range m.latestTodos {
		if t.Status != "completed" {
			incomplete = true
			break
		}
	}
	// Nothing outstanding AND the turn is over → done. This deliberately does NOT require a to-do
	// list: most sessions (plain chat, one-shot asks) never write one, and demanding total > 0 sent
	// every one of them through to the "give it room" fallback below, where they reported "working"
	// for the rest of their life. An idle session is the single most common state there is; calling
	// it working is the heartbeat's own version of the immortal Live pill.
	if !incomplete && (m.turnEnded || m.lastStatus == protocol.StatusIdle || m.lastStatus == protocol.StatusDone) {
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
