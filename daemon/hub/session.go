package hub

import (
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// Fan-out and transcript limits. broadcast() runs on the single run() goroutine that
// drains the provider event stream, so it must never block on a slow socket: each
// subscriber owns a bounded outbound queue drained by its own writer goroutine, and a
// subscriber whose queue overflows is dropped rather than allowed to stall the pump.
// The transcript is capped so a long-lived session can't grow memory without bound.
const (
	outboundBuffer      = 512     // per-subscriber queued events before it is dropped
	maxTranscriptEvents = 2048    // ring-buffer cap on retained events (by count)
	maxTranscriptBytes  = 8 << 20 // ring-buffer cap on retained events (by total bytes)
)

// managedSession is a hub-owned agent session shared by every subscribed client.
// A single run() goroutine reads the provider's event stream once, records it to a
// transcript (so late joiners can be caught up), and broadcasts each event to all
// current subscribers. This is the single-session-broadcast model: one provider
// subscription, many client observers — the daemon is the fan-out point.
type managedSession struct {
	hub  *Hub
	sess agent.Session
	meta sessionMeta // grouping info (project/cwd/worktree) for session.list + create

	mu              sync.Mutex
	subs            map[*transport.Conn]*subscriber
	transcript      [][]byte  // encoded protocol events, replayed to new subscribers
	transcriptBytes int       // running size of transcript (for the byte cap)
	lastActivity    time.Time // last event time; surfaced as Session.UpdatedAt for sorting/relative time
	inTok, outTok   int       // cumulative token usage across the session
	costUSD         float64   // cumulative cost (USD)
	wasRunning      bool      // saw activity since the last idle (gates the "finished" push)

	// Heartbeat supervision state (recorded from the event pump; read by the heartbeat tick).
	lastStatus       string          // last session.status ("running"/"idle"/"awaiting_approval"/"error")
	latestTodos      []protocol.Todo // last session.todos (completion signal)
	turnEnded        bool            // true after idle, false after running (turn boundary vs done)
	pendingApprovals int             // outstanding approval requests (never nudge while > 0)
	autonomous       bool            // opt-in: heartbeat may auto-nudge this session to continue
	nudgeCount       int             // nudges spent this session (capped by maxNudges)
	lastNudge        time.Time       // for the nudge cooldown
	lastCheckpoint   int             // token count at the last handoff-checkpoint nudge
	hbState          string          // last derived heartbeat state (for change detection)
	maxNudges        int             // give-up bound (0 = default)
	budgetUSD        float64         // cost ceiling for autonomous nudging (0 = default)
}

// subscriber owns one client's outbound queue plus the writer goroutine that drains it.
// broadcast enqueues without blocking; the writer performs the (blocking) encrypted
// Send. This decouples a slow/wedged socket from the session's event pump.
type subscriber struct {
	conn      *transport.Conn
	ch        chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (s *subscriber) close() { s.closeOnce.Do(func() { close(s.done) }) }

// sessionMeta is where a session runs, so clients can group the sidebar.
type sessionMeta struct {
	label         string // user-set session name (session.rename); overrides the derived title
	projectID     string
	cwd           string
	workspaceName string
	branch        string
	worktreePath  string // set when this session runs in a git worktree (for cleanup)
	baseCommit    string // repo HEAD when the worktree was created (stable diff base)
	repoRoot      string // main repo root (for worktree remove/prune)
	port          int    // port allocated to this worktree by a setup hook (0 = none)
	issueID       string // the ticket this session works (for write-back)
	issueKey      string // human ticket id (ENG-42)
	issueProvider string // "linear" | "jira"
}

func newManagedSession(h *Hub, sess agent.Session, meta sessionMeta) *managedSession {
	return &managedSession{hub: h, sess: sess, meta: meta, subs: map[*transport.Conn]*subscriber{}, lastActivity: time.Now()}
}

// onStatus fires "walk away" push notifications on turn boundaries: an agent that produced
// work then went idle → "finished"; a status error → "error". Gated by wasRunning so a bare
// idle (no activity) doesn't notify, which rate-limits to once per active turn.
func (m *managedSession) onStatus(ss protocol.SessionStatus) {
	m.mu.Lock()
	label := m.meta.label
	if label == "" {
		label = m.meta.workspaceName
	}
	switch ss.Status {
	case protocol.StatusRunning:
		m.wasRunning = true
		m.mu.Unlock()
	case protocol.StatusIdle, protocol.StatusDone:
		finished := m.wasRunning
		m.wasRunning = false
		m.mu.Unlock()
		if finished {
			m.hub.pushAgentFinished(m.sess.ID(), label)
		}
	case protocol.StatusError:
		m.wasRunning = false
		m.mu.Unlock()
		m.hub.pushAgentError(m.sess.ID(), label, ss.Detail)
	default:
		m.mu.Unlock()
	}
}

// lastActive is the unix time of the session's last event — the liveness clock the DB TTL
// prunes against (so a session with no activity for the TTL window ages out of the store).
func (m *managedSession) lastActive() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastActivity.Unix()
}

// info renders the session's identity + grouping metadata for the wire.
func (m *managedSession) info() protocol.Session {
	m.mu.Lock()
	updated := m.lastActivity.Unix()
	label := m.meta.label
	inTok, outTok, cost := m.inTok, m.outTok, m.costUSD
	m.mu.Unlock()
	return protocol.Session{
		ID:            m.sess.ID(),
		Provider:      m.sess.Provider(),
		Status:        protocol.StatusRunning,
		Name:          label,
		ProjectID:     m.meta.projectID,
		Cwd:           m.meta.cwd,
		WorkspaceName: m.meta.workspaceName,
		Branch:        m.meta.branch,
		Port:          m.meta.port,
		IssueKey:      m.meta.issueKey,
		IssueID:       m.meta.issueID,
		UpdatedAt:     updated,
		InputTokens:   inTok,
		OutputTokens:  outTok,
		CostUSD:       cost,
	}
}

// subscribe adds a client and replays the transcript so it sees the whole session.
// The subscriber is registered and the transcript snapshotted together under the lock,
// so no live event can slip between the snapshot and registration (each event lands in
// exactly one of replay or the live queue). A dedicated writer goroutine then delivers
// the replay followed by live events, so no client's socket blocks the event pump.
func (m *managedSession) subscribe(conn *transport.Conn) {
	m.mu.Lock()
	if _, ok := m.subs[conn]; ok {
		m.mu.Unlock()
		return
	}
	s := &subscriber{conn: conn, ch: make(chan []byte, outboundBuffer), done: make(chan struct{})}
	m.subs[conn] = s
	replay := append([][]byte(nil), m.transcript...)
	m.mu.Unlock()
	go m.writeLoop(s, replay)
}

// writeLoop delivers the transcript snapshot, then live events, until the subscriber is
// dropped or the client disconnects. It is the only goroutine that writes to conn here.
func (m *managedSession) writeLoop(s *subscriber, replay [][]byte) {
	for _, raw := range replay {
		select {
		case <-s.done:
			return
		default:
		}
		if s.conn.Send(raw) != nil {
			m.drop(s)
			return
		}
	}
	for {
		select {
		case raw := <-s.ch:
			if s.conn.Send(raw) != nil {
				m.drop(s)
				return
			}
		case <-s.done:
			return
		}
	}
}

func (m *managedSession) unsubscribe(conn *transport.Conn) {
	m.mu.Lock()
	s := m.subs[conn]
	delete(m.subs, conn)
	m.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// drop removes a subscriber whose outbound queue overflowed or whose socket errored,
// so one wedged client never blocks the pump or other subscribers.
func (m *managedSession) drop(s *subscriber) {
	m.mu.Lock()
	if m.subs[s.conn] == s {
		delete(m.subs, s.conn)
	}
	m.mu.Unlock()
	s.close()
}

// broadcast records the event and enqueues it to every current subscriber without
// blocking: a subscriber whose bounded queue is full is dropped rather than allowed to
// stall the run() goroutine that pumps the provider's event stream.
func (m *managedSession) broadcast(raw []byte) {
	m.mu.Lock()
	m.transcript = append(m.transcript, raw)
	m.transcriptBytes += len(raw)
	m.lastActivity = time.Now()
	m.trimTranscript()
	subs := make([]*subscriber, 0, len(m.subs))
	for _, s := range m.subs {
		subs = append(subs, s)
	}
	m.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- raw:
		default:
			m.drop(s) // slow client: drop it rather than block delivery to everyone else
		}
	}
}

// trimTranscript enforces the retention cap (by event count and total bytes), dropping
// the oldest events. Caller must hold m.mu.
func (m *managedSession) trimTranscript() {
	for len(m.transcript) > 0 && (len(m.transcript) > maxTranscriptEvents || m.transcriptBytes > maxTranscriptBytes) {
		m.transcriptBytes -= len(m.transcript[0])
		m.transcript[0] = nil // release the backing bytes for GC
		m.transcript = m.transcript[1:]
	}
}

// run pumps the session's events until it ends: records approval ownership + pushes,
// then broadcasts every event to all subscribers.
func (m *managedSession) run() {
	for ev := range m.sess.Events() {
		if ev.Type == protocol.TypeApprovalRequest {
			if ar, ok := ev.Payload.(protocol.ApprovalRequest); ok {
				m.hub.recordApproval(ar.ApprovalID, m)
				m.mu.Lock()
				m.pendingApprovals++
				m.mu.Unlock()
				m.hub.pushApproval(ar)
			}
		}
		if ev.Type == protocol.TypeSessionUsage {
			if u, ok := ev.Payload.(protocol.SessionUsage); ok {
				m.mu.Lock()
				m.inTok += u.InputTokens
				m.outTok += u.OutputTokens
				m.costUSD += u.CostUSD
				m.mu.Unlock()
			}
		}
		if ev.Type == protocol.TypeSessionTodos {
			if t, ok := ev.Payload.(protocol.SessionTodos); ok {
				m.mu.Lock()
				m.latestTodos = t.Todos
				m.mu.Unlock()
			}
		}
		if ev.Type == protocol.TypeOutputDelta || ev.Type == protocol.TypeThinking {
			m.mu.Lock()
			m.wasRunning = true
			m.mu.Unlock()
		}
		if ev.Type == protocol.TypeSessionStatus {
			if ss, ok := ev.Payload.(protocol.SessionStatus); ok {
				m.mu.Lock()
				m.lastStatus = ss.Status
				switch ss.Status {
				case protocol.StatusRunning:
					m.turnEnded = false
				case protocol.StatusIdle, protocol.StatusDone:
					m.turnEnded = true
				}
				m.mu.Unlock()
				m.onStatus(ss)
			}
		}
		raw, err := ev.Encode()
		if err != nil {
			continue
		}
		m.broadcast(raw)
	}
	m.hub.removeSession(m.sess.ID())
}
