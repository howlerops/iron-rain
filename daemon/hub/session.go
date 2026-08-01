package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/genui"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/transcript"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/worktree"
)

// responseTimeout bounds how long a sent prompt may produce ZERO provider events before the daemon
// surfaces a "no response" error. A wrong-directory opencode send is accepted (2xx) yet yields no
// events; without this the user waits on the 30-minute POST timeout. Short enough to catch a dead
// send fast, long enough that a model simply thinking before its first token isn't falsely flagged.
const responseTimeout = 25 * time.Second

// replayGrace is how long after a session binding is created a self-replaying provider is assumed to
// still be re-streaming its history. Inside it the durable transcript is withheld (the provider is
// authoritative and layering both duplicates every message); outside it the provider will never
// re-stream again, so the durable transcript is the only history a new subscriber can get.
const replayGrace = 15 * time.Second

// replayTailLimit bounds the events a subscribe replays. Everything older is reachable through
// transcript.page. Large enough that most conversations arrive whole, small enough that the biggest
// ones still open instantly.
const replayTailLimit = 200

// armResponseWatchdog marks the session as awaiting a first event and, if none arrives within
// responseTimeout, synthesizes a StatusError so every client sees it (and it lands in the durable
// transcript). The generation counter makes a newer prompt/response cancel an older watchdog.
func (m *managedSession) armResponseWatchdog() {
	m.mu.Lock()
	m.awaitingResponse = true
	m.respWatchdogGen++
	gen := m.respWatchdogGen
	m.mu.Unlock()
	go func() {
		time.Sleep(responseTimeout)
		m.mu.Lock()
		fired := m.awaitingResponse && m.respWatchdogGen == gen
		if fired {
			m.awaitingResponse = false
		}
		m.mu.Unlock()
		if !fired {
			return
		}
		detail := "No response from the agent — your message may not have reached it (a directory mismatch can accept the send but route it nowhere). Your prompt was saved; try “Recover session”, then resend."
		log.Printf("session %s (%s): NO RESPONSE within %s of a prompt — surfacing to clients", m.sess.ID(), m.sess.Provider(), responseTimeout)
		if t := m.hub.tel(); t != nil {
			t.Record("session.no_response", m.sess.Provider(), responseTimeout, fmt.Errorf("no event after prompt"))
		}
		_ = m.hub.tr().Append(m.sess.ID(), transcript.Entry{Kind: "status", Text: "error", Detail: detail})
		ss := protocol.SessionStatus{SessionID: m.sess.ID(), Status: protocol.StatusError, Detail: detail}
		if raw, err := (agent.Event{Type: protocol.TypeSessionStatus, Payload: ss}).Encode(); err == nil {
			m.broadcast(raw)
		}
	}()
}

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

	mu               sync.Mutex
	subs             map[*transport.Conn]*subscriber
	transcript       [][]byte  // encoded protocol events, replayed to new subscribers
	transcriptBytes  int       // running size of transcript (for the byte cap)
	lastActivity     time.Time // last event time; surfaced as Session.UpdatedAt for sorting/relative time
	inTok, outTok    int       // cumulative token usage across the session
	costUSD          float64   // cumulative cost (USD)
	wasRunning       bool      // saw activity since the last idle (gates the "finished" push)
	turnStartedAt    time.Time // when the current turn started running (for the "finished" push duration)
	loopDoneNotified bool      // fired the "loop run finished" push once (loop sessions only)

	// Turn Engine state (see turn.go) — guarded by m.mu. turnPhase "" = no open turn.
	turnID         string
	turnPhase      string // running | awaiting_approval while open
	turnStarted    time.Time
	turnLastEvent  time.Time
	turnDetail     string
	turnKids       map[string]*protocol.TurnChild
	turnStopLoop   chan struct{}
	turnProbeFails int

	// Durable-transcript state (touched ONLY by the run() goroutine, so no lock needed): a per-session
	// sequence for ordering persisted events (seeded past any restored rows), the accumulated assistant
	// delta text for the current turn, and whether a real assistant message was already persisted this
	// turn (so the synthetic delta-accumulated one isn't a duplicate).
	// txSeq orders a session's durable rows. It is written from the provider pump AND from the hub
	// goroutine (the user-prompt echo), so it has its own lock — the "run()-goroutine only" comment
	// that used to justify going unguarded stopped being true the moment user messages were persisted.
	// Turn Engine timings, copied from the package defaults at construction. They live HERE rather
	// than being read from package variables because tests shrink them, and a package variable written
	// by one test while another test's turn loop is still reading it is a data race — one that fails
	// `go test -race` and therefore blocks every release.
	hbEvery        time.Duration
	quietAfter     time.Duration
	reconcileTick  time.Duration
	probeFailLimit int

	txMu  sync.Mutex
	txSeq int64
	// transcriptTrimmed records that the in-memory ring has DROPPED events. Once true, the ring is no
	// longer a complete record of the session and must not be replayed as if it were.
	transcriptTrimmed bool
	// replayTotal is how many events the last assembled history held, so a page request knows how far
	// back it can go without re-deciding what "the history" is.
	replayTotal   int
	asstAccum     strings.Builder
	asstPersisted bool

	// ringFromStart reports whether m.transcript holds the session's history from its FIRST event.
	// False for any session this process ATTACHED to rather than created — a restored session's ring
	// starts empty and then fills with only what happens from now on, which is not the conversation.
	ringFromStart bool

	// createdAt is when this binding was made (create or attach). It bounds the window in which a
	// self-replaying provider might still be re-streaming its history — see subscribe().
	createdAt time.Time

	// Heartbeat supervision state (recorded from the event pump; read by the heartbeat tick).
	lastStatus       string          // last session.status ("running"/"idle"/"awaiting_approval"/"error")
	latestTodos      []protocol.Todo // last session.todos (completion signal)
	turnEnded        bool            // true after idle, false after running (turn boundary vs done)
	pendingApprovals int             // outstanding approval requests (never nudge while > 0)
	mode             string          // code | ask | architect — enforced daemon-side (see modes.go)
	autonomous       bool            // opt-in: heartbeat may auto-nudge this session to continue
	nudgeCount       int             // nudges spent this session (capped by maxNudges)
	lastNudge        time.Time       // for the nudge cooldown
	lastCheckpoint   int             // token count at the last handoff-checkpoint nudge
	hbState          string          // last derived heartbeat state (for change detection)
	maxNudges        int             // give-up bound (0 = default)
	budgetUSD        float64         // cost ceiling for autonomous nudging (0 = default)
	lastHandoffMtime int64           // mtime of the handoff file at last index (skip re-index if unchanged)
	model            string          // active model id ("" = provider default)
	modelProvider    string          // sub-provider/backend for the model
	pendingContext   string          // one-shot note prepended to the FIRST user prompt (multi-repo layout)

	seg genui.Segmenter // incremental scanner for ```iron:ui``` generative-UI fences in assistant text

	awaitingResponse bool                  // a prompt was sent and no event has come back yet (drives the no-response watchdog)
	respWatchdogGen  int                   // generation counter so a stale watchdog can't fire after a newer prompt/response
	userStopped      bool                  // the user explicitly stopped/removed this session (vs. an unexpected provider exit)
	conflicted       bool                  // this worktree session's branch would conflict with the default branch (passive badge)
	checkpoints      []protocol.Checkpoint // restore points snapshotted for this worktree session (newest last)
}

// markUserStopped records that the session's close is user-intended, so run()'s cleanup DELETES the
// durable record instead of preserving it. Without this, an unexpected provider exit (crashed
// claude-code sidecar / exited CLI) is indistinguishable from a stop and its record is wrongly dropped.
func (m *managedSession) markUserStopped() {
	m.mu.Lock()
	m.userStopped = true
	m.mu.Unlock()
}

// activityTitle is a short human label for the session in the activity feed: the user-set name,
// else a repo/branch-ish hint from the cwd, else a short id.
func (m *managedSession) activityTitle() string {
	if m.meta.label != "" {
		return m.meta.label
	}
	if m.meta.branch != "" {
		return m.meta.branch
	}
	if m.meta.cwd != "" {
		return filepath.Base(m.meta.cwd)
	}
	id := m.sess.ID()
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// subscriber owns one client's outbound queue plus the writer goroutine that drains it.
// broadcast enqueues without blocking; the writer performs the (blocking) encrypted
// Send. This decouples a slow/wedged socket from the session's event pump.
type subscriber struct {
	conn      *transport.Conn
	ch        chan []byte
	done      chan struct{}
	closeOnce sync.Once

	// delivered holds a hash of every frame this subscriber already received in its replay, for a
	// short window after it subscribed.
	//
	// The replay is assembled from a SNAPSHOT of the ring. A self-replaying provider (opencode,
	// claude-code) pushes its own history through broadcast AFTER that snapshot is taken — on
	// session.recover, and on an attach for a session the daemon could not re-attach at startup — so
	// de-duplicating the snapshot alone is not enough and the client sees its conversation twice. The
	// daemon owes the client a transcript with no repeats however the frames reach it.
	mu        sync.Mutex
	delivered map[string]struct{}
	dedupTill time.Time
}

// seen reports whether this exact frame was already delivered in the replay, and stops tracking once
// the re-stream window has passed so the map cannot grow for the life of the connection.
func (s *subscriber) seen(raw []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered == nil {
		return false
	}
	if time.Now().After(s.dedupTill) {
		s.delivered = nil // window over: a repeat now is genuinely a repeat
		return false
	}
	h := sha256.Sum256(raw)
	k := string(h[:])
	if _, dup := s.delivered[k]; dup {
		delete(s.delivered, k) // one replay frame suppresses exactly one re-stream copy
		return true
	}
	return false
}

func (s *subscriber) rememberReplay(frames [][]byte, window time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered = make(map[string]struct{}, len(frames))
	for _, f := range frames {
		h := sha256.Sum256(f)
		s.delivered[string(h[:])] = struct{}{}
	}
	s.dedupTill = time.Now().Add(window)
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

	// providerURL is the server this session was ATTACHED to (opencode's HTTP base, say). Without it
	// a taken-over session cannot be reconstructed after a daemon restart: the restore knows the
	// session id but not where it lives, so it reopens empty or against the wrong server.
	providerURL string

	// Cross-repo workspace: one worktree per member repo, all under cwd (the layout dir). Empty
	// for single-repo/shared sessions. Drives the fs guard, session file tree, and workspace.diff.
	members []worktree.Member

	// Explicit code-view roots — the exact folders the user picked. Set for multi-repo SHARED
	// sessions (cwd is their common ancestor, which must NOT expose sibling folders). When set,
	// sessionRoots returns these instead of cwd. Empty = derive from members/cwd.
	roots []string

	// Scoped child session: the parent it was delegated from + the subtask it owns. Empty for
	// top-level sessions. Lets the app group children under their parent and label them.
	parentID string
	subtask  string

	// Fan-out grouping: when this session is one of N racing the same prompt, fanoutGroup is the
	// shared id and fanoutVariant its 0-based index.
	fanoutGroup   string
	fanoutVariant int

	// loopName is set when this session is a run of a recurring autonomous loop — used to fire the
	// "loop run finished" push once the run completes.
	loopName string

	// ephemeral: a scratch "just chat" session — no project, NOT persisted to the store.
	ephemeral bool
}

func newManagedSession(h *Hub, sess agent.Session, meta sessionMeta) *managedSession {
	now := time.Now()
	return &managedSession{hub: h, sess: sess, meta: meta, subs: map[*transport.Conn]*subscriber{},
		lastActivity: now, createdAt: now,
		hbEvery: turnHeartbeatEvery, quietAfter: turnQuietAfter,
		reconcileTick: turnReconcileTick, probeFailLimit: turnProbeFailLimit}
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
		if !m.wasRunning {
			m.turnStartedAt = time.Now() // start of a fresh turn — time it for the finished summary
		}
		m.wasRunning = true
		m.mu.Unlock()
	case protocol.StatusIdle, protocol.StatusDone:
		finished := m.wasRunning
		m.wasRunning = false
		// Capture a compact summary for the "finished" push: how long it ran, to-do progress, spend.
		var dur time.Duration
		if !m.turnStartedAt.IsZero() {
			dur = time.Since(m.turnStartedAt)
		}
		done, total := 0, len(m.latestTodos)
		for _, td := range m.latestTodos {
			if td.Status == "completed" {
				done++
			}
		}
		cost := m.costUSD
		group := m.meta.fanoutGroup
		loopName := m.meta.loopName
		loopDone := loopName != "" && !m.loopDoneNotified && finished
		if loopDone {
			m.loopDoneNotified = true
		}
		m.mu.Unlock()
		if finished {
			m.hub.pushAgentFinished(m.sess.ID(), label, dur, done, total, cost)
		}
		if group != "" {
			m.hub.checkFanoutDone(group) // last variant idle → "fan-out finished"
		}
		if loopDone {
			m.hub.pushLoopDone(m.sess.ID(), loopName) // the loop run's work completed
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
// seedStatus sets the initial rendered status for a session that was ATTACHED/RESTORED rather than
// freshly created — so it doesn't fall through info()'s "no status yet → running" default and appear
// stuck "working". Safe to call before run() starts; the first real status event overrides it.
func (m *managedSession) seedStatus(status string) {
	m.mu.Lock()
	m.lastStatus = status
	if status == protocol.StatusIdle || status == protocol.StatusDone {
		m.turnEnded = true
	}
	m.mu.Unlock()
}

func (m *managedSession) info() protocol.Session {
	m.mu.Lock()
	updated := m.lastActivity.Unix()
	label := m.meta.label
	inTok, outTok, cost := m.inTok, m.outTok, m.costUSD
	isWorkspace := len(m.meta.members) > 0
	status := m.lastStatus
	model, modelProvider := m.model, m.modelProvider
	mode := m.mode
	conflicted := m.conflicted
	m.mu.Unlock()
	if status == "" {
		status = protocol.StatusRunning // freshly created — no status event yet
	}
	return protocol.Session{
		ID:            m.sess.ID(),
		Provider:      m.sess.Provider(),
		Status:        status, // real last status (idle/error/awaiting_approval), not a hardcoded "running"
		Name:          label,
		ProjectID:     m.meta.projectID,
		Cwd:           m.meta.cwd,
		WorkspaceName: m.meta.workspaceName,
		Branch:        m.meta.branch,
		IsWorkspace:   isWorkspace,
		ParentID:      m.meta.parentID,
		Subtask:       m.meta.subtask,
		Port:          m.meta.port,
		IssueKey:      m.meta.issueKey,
		IssueID:       m.meta.issueID,
		Model:         model,
		ModelProvider: modelProvider,
		Mode:          mode,
		UpdatedAt:     updated,
		InputTokens:   inTok,
		OutputTokens:  outTok,
		CostUSD:       cost,
		Conflicted:    conflicted,
		FanoutGroup:   m.meta.fanoutGroup,
		FanoutVariant: m.meta.fanoutVariant,
		Ephemeral:     m.meta.ephemeral,
	}
}

// subscribe adds a client and replays the transcript so it sees the whole session.
// The subscriber is registered and the transcript snapshotted together under the lock,
// so no live event can slip between the snapshot and registration (each event lands in
// exactly one of replay or the live queue). A dedicated writer goroutine then delivers
// the replay followed by live events, so no client's socket blocks the event pump.
// ringStreamedText concatenates every streamed token in the ring, so a durable frame can be tested
// for "the ring already says this".
func ringStreamedText(ring [][]byte) string {
	var b strings.Builder
	for _, r := range ring {
		var f struct {
			Type    string `json:"type"`
			Payload struct {
				Text string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(r, &f) != nil || f.Type != protocol.TypeOutputDelta {
			continue
		}
		b.WriteString(f.Payload.Text)
	}
	return b.String()
}

// isSyntheticAssistantEcho reports whether a durable frame is the end-of-turn message the daemon
// synthesised from streamed text that the ring already carries.
func isSyntheticAssistantEcho(raw []byte, streamed string) bool {
	var f struct {
		Type    string `json:"type"`
		Payload struct {
			Role  string `json:"role"`
			Text  string `json:"text"`
			MsgID string `json:"msg_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return false
	}
	if f.Type != protocol.TypeSessionMessage || f.Payload.Role != "assistant" || f.Payload.MsgID != "" {
		return false // a provider's real message always carries an id
	}
	text := strings.TrimSpace(f.Payload.Text)
	return text != "" && strings.Contains(streamed, text)
}

// joinHistory merges the durable transcript into the in-memory ring IN BROADCAST ORDER, without
// emitting any frame twice.
//
// Order is part of the history, not a detail. The ring carries frames the durable store never sees —
// the user-prompt echo, output/thinking deltas, ui.component, session.subagent, and tool cards in
// their `running` state — and those are only meaningful in position. A `running` card emitted after
// its `completed` twin reverts a finished tool to a spinner; deltas emitted after their finalized
// message render the reply a second time. An earlier version concatenated the two sources (all
// durable, then whatever the ring had left over) and did exactly that to every restored session that
// had since run a turn.
//
// So the join walks the RING and emits each durable frame at its ring position. Durable frames with
// no ring counterpart are older than the ring window and form the prefix.
//
// Matching is by exact bytes, because both sides ARE the same bytes: broadcast hands one slice to the
// ring and the store keeps it verbatim. The walk is position-aware rather than set-based so a
// genuinely repeated event (the same prompt twice, with no id to tell them apart) survives as two.
func joinHistory(durable, ring [][]byte) [][]byte {
	if len(ring) == 0 {
		return durable
	}
	if len(durable) == 0 {
		return ring
	}
	// Where each durable frame sits in the ring, if at all. Duplicate byte sequences consume ring
	// positions in order, so repeats line up one-for-one instead of all matching the first.
	ringAt := make(map[string][]int, len(ring))
	for i, r := range ring {
		h := sha256.Sum256(r)
		k := string(h[:])
		ringAt[k] = append(ringAt[k], i)
	}
	type placed struct{ pos, seq int }
	// pos = ring index this durable frame maps to; -1 means "older than the ring".
	mapped := make([]placed, len(durable))
	for i, d := range durable {
		h := sha256.Sum256(d)
		k := string(h[:])
		if idxs := ringAt[k]; len(idxs) > 0 {
			mapped[i] = placed{pos: idxs[0], seq: i}
			ringAt[k] = idxs[1:]
		} else {
			mapped[i] = placed{pos: -1, seq: i}
		}
	}
	// LEGACY DATA ONLY. The synthetic end-of-turn message is now broadcast like every other frame, so
	// it has a ring twin and is matched by bytes above — this branch is unreachable for anything
	// written since. Transcripts recorded BEFORE that change still hold synthetic rows with no ring
	// counterpart, and without this they would render the reply twice. Identifiable as an assistant
	// message with no message id; a provider's real message always carries one.
	streamed := ringStreamedText(ring)
	// Frames that carry their own identity — generative-UI cards, sub-agent rows — can appear in BOTH
	// sources with DIFFERENT bytes, because their state advances: a card is emitted `running` and
	// updated to `ready`, so the durable copy from an earlier run and the freshly re-derived ring copy
	// are the same card in two states. Byte matching cannot see that, and served both. Match these by
	// identity and let the RING win, since it is the newer state.
	ringIDs := make(map[string]struct{}, len(ring))
	for _, r := range ring {
		var typ struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(r, &typ) != nil {
			continue
		}
		if id := renderableID(typ.Type, r); id != "" {
			ringIDs[id] = struct{}{}
		}
	}
	out := make([][]byte, 0, len(durable)+len(ring))
	// Durable history older than the ring leads, in its own order.
	for i, mp := range mapped {
		if mp.pos >= 0 {
			continue
		}
		if streamed != "" && isSyntheticAssistantEcho(durable[i], streamed) {
			continue // the ring already carries this reply as the deltas it was built from
		}
		var typ struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(durable[i], &typ) == nil {
			if id := renderableID(typ.Type, durable[i]); id != "" {
				if _, live := ringIDs[id]; live {
					continue // the ring has a fresher copy of this exact card
				}
			}
		}
		out = append(out, durable[i])
	}
	// Then the ring, in ring order — which already contains the durable frames that overlap it.
	out = append(out, ring...)
	return out
}

// replayFrames assembles what a new subscriber is sent: the session's history oldest-first, capped to
// the tail. Split out of subscribe so the durable-vs-ring decision — the part that has now twice
// silently lost a conversation — is directly testable.
func (m *managedSession) replayFrames() [][]byte {
	replay := stripNonReplayable(m.fullHistory())
	return boundTail(replay, replayTailLimit)
}

// stripNonReplayable drops frames that are wrong to REPLAY even though they were right to broadcast.
//
// session.usage is the whole list today: its client handler ACCUMULATES into a running total, so
// replaying a session's usage frames on every open inflates the cost meter without bound. The
// authoritative totals ride on the OK reply to session.info instead.
func stripNonReplayable(frames [][]byte) [][]byte {
	out := make([][]byte, 0, len(frames))
	for _, f := range frames {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(f, &head) == nil && head.Type == protocol.TypeSessionUsage {
			continue
		}
		out = append(out, f)
	}
	return out
}

// boundTail caps a replay to its most recent frames WITHOUT letting the cap consume everything that
// carries standalone meaning.
//
// A naive `replay[len-limit:]` looks right until a single streamed reply contributes hundreds of
// output.delta frames: the tail then lands entirely inside that delta run, every session.message is
// sliced off, and the client renders an empty conversation — the exact symptom this whole line of
// work set out to fix. So the cap counts frames that stand on their own (messages, tool cards, UI
// components) and keeps everything from the limit-th most recent of those onward.
func boundTail(replay [][]byte, limit int) [][]byte {
	if len(replay) <= limit {
		return replay
	}
	var head struct {
		Type string `json:"type"`
	}
	standalone := 0
	for i := len(replay) - 1; i >= 0; i-- {
		if json.Unmarshal(replay[i], &head) == nil {
			switch head.Type {
			case protocol.TypeSessionMessage, protocol.TypeSessionTool, protocol.TypeUIComponent:
				standalone++
			}
		}
		if standalone >= limit {
			return replay[i:]
		}
	}
	return replay
}

func (m *managedSession) subscribe(conn *transport.Conn) {
	m.mu.Lock()
	existing, already := m.subs[conn]
	s := existing
	if !already {
		s = &subscriber{conn: conn, ch: make(chan []byte, outboundBuffer), done: make(chan struct{})}
		m.subs[conn] = s
	}
	m.mu.Unlock()
	replay := m.replayFrames()
	// Remember what this subscriber is about to receive, so a provider re-stream arriving as LIVE
	// traffic in the next few seconds is suppressed instead of doubling the conversation on screen.
	s.rememberReplay(replay, replayGrace)

	// Deliver the CURRENT turn snapshot to this subscriber: turn.state is transient (never replayed
	// from the transcript), so without this a client that subscribes mid-turn — including the CREATOR
	// of a session started with a prompt, and any session switch — would see no turn state until the
	// next ~10s heartbeat (or, for a fast turn, only the terminal frame with no `running` before it).
	m.mu.Lock()
	if m.turnPhase != "" {
		ts := m.turnSnapshotLocked(m.turnPhase, "")
		m.mu.Unlock()
		if raw, err := (agent.Event{Type: protocol.TypeTurnState, Payload: ts}).Encode(); err == nil {
			replay = append(replay, raw)
		}
	} else {
		m.mu.Unlock()
	}
	if already {
		// The client RE-subscribed to a session it already had a subscription for — this is a session
		// SWITCH (the app cleared its transcript view on open, or switched away and back). Previously we
		// returned early here with NO replay, so the pane stayed blank and "the data never reloaded".
		// Re-send the transcript to the SAME subscriber (its writeLoop drains s.ch) so the conversation
		// repopulates. Runs in a goroutine so a full outbound buffer can't stall the event pump.
		go func() {
			for _, raw := range replay {
				select {
				case s.ch <- raw:
				case <-s.done:
					return
				}
			}
		}()
		return
	}
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

// emitUIComponents broadcasts each generative-UI component the segmenter produced as its own
// ui.component event (stamping the session id). Called from the event pump on assistant deltas.
func (m *managedSession) emitUIComponents(sessionID string, comps []protocol.UIComponent) {
	for _, c := range comps {
		c.SessionID = sessionID
		if raw, err := (agent.Event{Type: protocol.TypeUIComponent, Payload: c}).Encode(); err == nil {
			m.persistRenderable(protocol.TypeUIComponent, raw)
			m.broadcast(raw)
		}
	}
}

// flushUI finalizes the generative-UI segmenter at turn end: it emits any component/text held in a
// closing fence and resets the segmenter for the next turn. Called on idle/done.
func (m *managedSession) flushUI(sessionID string) {
	fwd, comps := m.seg.Flush()
	m.emitUIComponents(sessionID, comps)
	if fwd != "" {
		if raw, err := (agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: sessionID, Text: fwd}}).Encode(); err == nil {
			m.broadcast(raw)
		}
	}
	m.seg = genui.Segmenter{}
}

// ownEvent reports whether a delta/thinking event belongs to the session itself (not a sub-agent).
func ownEvent(ev agent.Event, sid string) bool {
	switch p := ev.Payload.(type) {
	case protocol.OutputDelta:
		return p.SessionID == sid
	case protocol.Thinking:
		return p.SessionID == sid
	}
	return false
}

// persistDurable writes a finalized transcript event to the durable store (SQLite) so a session's
// history survives daemon restarts and ring-buffer trimming — for EVERY provider. Raw ~40ms deltas
// are ACCUMULATED (not written per-token); only finalized messages / completed tool cards / errors
// are written, keyed by the provider's message id (when known) for cross-restart dedup. Scoped to the
// PARENT session's own events. run()-goroutine only (no lock needed for txSeq/asst*).
func (m *managedSession) persistDurable(ev agent.Event, raw []byte) {
	db := m.hub.db
	if db == nil || m.meta.ephemeral {
		return // ephemeral scratch chats aren't persisted (keeps "no sessions row" == orphan for prune)
	}
	sid := m.sess.ID()
	var msgID string
	switch ev.Type {
	case protocol.TypeSessionMessage:
		msg, ok := ev.Payload.(protocol.SessionMessage)
		if !ok || msg.SessionID != sid {
			return
		}
		msgID = msg.MsgID
		if msg.Role == "assistant" {
			m.asstPersisted = true // a real assistant message → skip the synthetic delta one at turn end
		}
	case protocol.TypeSessionTool:
		t, ok := ev.Payload.(protocol.SessionTool)
		if !ok || t.SessionID != sid || (t.Status != "completed" && t.Status != "error") {
			return // only the final tool state is durable
		}
		msgID = "tool:" + t.ID
	case protocol.TypeSessionStatus:
		ss, ok := ev.Payload.(protocol.SessionStatus)
		if !ok || ss.SessionID != sid || ss.Status != protocol.StatusError {
			return
		}
		// error marker: NULL id (each distinct)
	case protocol.TypeOutputDelta:
		if d, ok := ev.Payload.(protocol.OutputDelta); ok && d.SessionID == sid {
			m.asstAccum.WriteString(d.Text) // accumulate the VISIBLE (post-fence) streamed text
		}
		return
	default:
		return
	}
	// The sequence ALWAYS advances. It is a position counter, not an identity.
	//
	// A previous version advanced it only when AppendTranscript reported a real insert, to avoid
	// "burning" numbers on rows deduplicated by message id. That was catastrophic: seq is half of
	// PRIMARY KEY(session_id, seq), so after a single dedup the counter stalled and every subsequent
	// event collided with an existing row and was silently dropped by INSERT OR IGNORE. One re-streamed
	// message was enough to stop a session persisting anything ever again — the whole conversation
	// from that point on was lost, with no error anywhere.
	//
	// Gaps in the sequence are harmless slack: nothing reads seq except ORDER BY. De-duplication is the
	// msg_id unique index's job, and it does it whether or not the number moved.
	m.appendDurable(sid, msgID, raw)
}

// finalizeTurnTranscript runs on idle: if the turn streamed assistant text but no finalized assistant
// SessionMessage was persisted (claude-code/pi/cli stream deltas only, never a finalized message),
// persist a synthetic one so the durable transcript actually contains the reply. Resets per-turn
// state. NULL msg id — those providers never re-stream history, so there's nothing to dedup against.
func (m *managedSession) finalizeTurnTranscript() {
	db := m.hub.db
	text := m.asstAccum.String()
	if db != nil && !m.asstPersisted && strings.TrimSpace(text) != "" {
		ev := agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: m.sess.ID(), Role: "assistant", Text: text}}
		if raw, err := ev.Encode(); err == nil {
			// BROADCAST it, don't just write it. Writing to SQLite without broadcasting created a
			// frame that existed in one source and not the other, and merging the two rendered every
			// reply twice. Every frame now takes the same path: broadcast (which rings it) and
			// persist. m.broadcast is the ring; appendDurable is the store.
			m.appendDurable(m.sess.ID(), "", raw)
			m.broadcast(raw)
		}
	}
	m.asstAccum.Reset()
	m.asstPersisted = false
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
		if s.seen(raw) {
			continue // already delivered in this subscriber's replay — a provider re-stream
		}
		select {
		case s.ch <- raw:
		default:
			m.drop(s) // slow client: drop it rather than block delivery to everyone else
		}
	}
}

// fullHistory assembles this session's complete replayable history: the durable transcript in front
// of the live ring when the ring has been trimmed to a window, otherwise just the ring (or the
// durable transcript when the ring is empty). Subscribe and transcript.page both go through here so
// a page can never disagree with what the initial replay showed.
// fullHistory is this session's complete history in broadcast order — the SAME array replayFrames
// bounds to a tail, and the coordinate space transcript.page indexes.
//
// It used to implement its own, older version of the source rule (durable only when the ring was
// empty or trimmed, plus a self-replay grace window). Paging therefore indexed a different array than
// the one the replay came from, so after a restart the "Show earlier messages" affordance the replay
// had just advertised could return nothing. One rule, one array, both callers.
func (m *managedSession) fullHistory() [][]byte {
	m.mu.Lock()
	ring := append([][]byte(nil), m.transcript...)
	trimmed := m.transcriptTrimmed
	fromStart := m.ringFromStart
	m.mu.Unlock()
	if fromStart && !trimmed {
		return ring // this process saw the session from its first event: the ring is the whole story
	}
	db := m.hub.db
	if db == nil {
		return ring
	}
	durable, err := db.Transcript(m.sess.ID())
	if err != nil || len(durable) == 0 {
		return ring
	}
	return joinHistory(durable, ring)
}

// historyPage returns the events immediately BEFORE the newest `loaded` ones, oldest-first, plus
// whether anything older still remains.
func (m *managedSession) historyPage(loaded, limit int) (page [][]byte, more bool) {
	all := m.fullHistory()
	end := len(all) - loaded
	if end <= 0 {
		return nil, false
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	return all[start:end], start > 0
}

// trimTranscript enforces the retention cap (by event count and total bytes), dropping
// the oldest events. Caller must hold m.mu.
func (m *managedSession) trimTranscript() {
	for len(m.transcript) > 0 && (len(m.transcript) > maxTranscriptEvents || m.transcriptBytes > maxTranscriptBytes) {
		m.transcriptBytes -= len(m.transcript[0])
		m.transcript[0] = nil // release the backing bytes for GC
		m.transcript = m.transcript[1:]
		m.transcriptTrimmed = true // the ring is now a WINDOW, not the whole session
	}
}

// run pumps the session's events until it ends: records approval ownership + pushes,
// then broadcasts every event to all subscribers.
func (m *managedSession) run() {
	// Continue the durable-transcript sequence past any rows persisted before a daemon restart, so new
	// events sort after the restored history instead of colliding with it.
	if db := m.hub.db; db != nil {
		if seq, err := db.MaxTranscriptSeq(m.sess.ID()); err == nil {
			m.txMu.Lock()
			m.txSeq = seq
			m.txMu.Unlock()
			// Durable rows already exist → this process is joining a conversation in progress, so its
			// ring can never be the whole story and subscribe must lead with the durable transcript.
			m.mu.Lock()
			m.ringFromStart = seq == 0
			m.mu.Unlock()
		}
	} else {
		m.mu.Lock()
		m.ringFromStart = true // no durable store: the ring is all there has ever been
		m.mu.Unlock()
	}
	for ev := range m.sess.Events() {
		m.noteTurnEvent() // every provider event = liveness for the Turn Engine
		if sa, ok := ev.Payload.(protocol.SubAgent); ok && ev.Type == protocol.TypeSessionSubAgent && sa.ParentID == m.sess.ID() {
			m.turnOnChild(sa) // sub-agents are children of the turn
		}
		// The turn is alive: any event back from the provider clears the no-response watchdog. A
		// wrong-directory send produces NO events, so it never reaches here and the watchdog fires.
		m.mu.Lock()
		if m.awaitingResponse {
			m.awaitingResponse = false
		}
		m.mu.Unlock()
		if ev.Type == protocol.TypeApprovalRequest {
			if ar, ok := ev.Payload.(protocol.ApprovalRequest); ok {
				// Read-only modes deny mutating tools outright, before any rule is consulted — a
				// standing "always allow bash" must not punch a hole through Ask mode.
				if mode := m.sessionMode(); modeDeniesTool(mode, ar.Tool) {
					log.Printf("session %s: denied %s — %s mode is read-only", m.sess.ID(), ar.Tool, mode)
					go func(id string) { _ = m.sess.Respond(context.Background(), id, protocol.DecisionDeny) }(ar.ApprovalID)
					m.emitTool("⊘ " + ar.Tool + " blocked — " + mode + " mode is read-only")
					continue
				}
				// A persisted rule answers it silently — permissions are asked ONCE, ever, not once
				// per session. The request never reaches a client.
				if m.hub.autoAllowApproval(m, ar) {
					continue
				}
				// Offer the scopes an ALWAYS could narrow to. Computed here, once, from what this
				// harness actually told us, so the client never has to parse a command itself.
				m.mu.Lock()
				projectID := m.meta.projectID
				m.mu.Unlock()
				ar.SuggestedScopes = suggestScopes(ar, ar.Patterns, projectID)
				ev.Payload = ar // forward the enriched request to clients, not the bare one
				m.hub.recordApproval(ar, m)
				m.mu.Lock()
				m.pendingApprovals++
				m.mu.Unlock()
				m.hub.pushApproval(ar)
			}
		}
		if ev.Type == protocol.TypeSessionUsage {
			if u, ok := ev.Payload.(protocol.SessionUsage); ok {
				// Persist the increment before folding it into the live meter. The live totals die
				// with the session; these rows are what make "today" and "this week" answerable at
				// all, and what a finished job cost.
				if db := m.hub.db; db != nil {
					m.mu.Lock()
					model, projectID := m.model, m.meta.projectID
					m.mu.Unlock()
					_ = db.AppendUsage(store.UsageEvent{
						SessionID: m.sess.ID(), Provider: m.sess.Provider(), Model: model,
						ProjectID: projectID, InTokens: u.InputTokens, OutTokens: u.OutputTokens,
						CostUSD: u.CostUSD,
					})
				}
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
				changed := todosChanged(m.latestTodos, t.Todos)
				m.latestTodos = t.Todos
				m.mu.Unlock()
				// PROJECTION: the daemon already has the structured list, so it builds the checklist
				// itself rather than waiting for a model to volunteer an iron:ui block. Only on a real
				// change — a harness that re-sends an identical list shouldn't redraw the card.
				if changed && t.SessionID == m.sess.ID() {
					if comp, ok := genui.ProjectTodos(m.sess.ID(), t.Todos); ok {
						m.emitUIComponents(m.sess.ID(), []protocol.UIComponent{comp})
					}
				}
			}
		}
		if ev.Type == protocol.TypeOutputDelta || ev.Type == protocol.TypeThinking {
			m.mu.Lock()
			m.wasRunning = true
			noTurn := m.turnPhase == ""
			m.mu.Unlock()
			// Fallback entry: output is streaming but no turn is open (a prompt path we didn't wire,
			// a nudge, a replayed live turn after re-attach) — the truth is "running", say so.
			if noTurn && ownEvent(ev, m.sess.ID()) {
				m.openTurn("")
			}
		}
		// NOTE: the durable transcript deliberately records only the user's WRITE-AHEAD prompts (at
		// send, in the hub) + error markers (below) — the irreplaceable, low-volume, never-replayed
		// data that the incident lost. Mirroring assistant replies here would re-append the provider's
		// full replayed history on every restart (unbounded bloat); full assistant-transcript
		// persistence needs replay-dedup and is tracked separately.
		if ev.Type == protocol.TypeSessionStatus {
			// Only the PARENT session's OWN status drives its turn tracking, logging, "finished"
			// activity, and onStatus. A sub-agent's forwarded status (SessionID == child id) would
			// otherwise flip the parent's turnEnded/lastStatus, spam turn-end logs + activity, and flush
			// the parent's UI fence every time a sub-agent idles. Child status is still broadcast below
			// (for its inline card) — it just must not masquerade as the parent's turn state.
			if ss, ok := ev.Payload.(protocol.SessionStatus); ok && (ss.SessionID == "" || ss.SessionID == m.sess.ID()) {
				m.mu.Lock()
				m.lastStatus = ss.Status
				startedTurn := false // log "turn start" only on the ended→running EDGE, not per-tool
				switch ss.Status {
				case protocol.StatusRunning:
					startedTurn = m.turnEnded || !m.wasRunning
					m.turnEnded = false
				case protocol.StatusIdle, protocol.StatusDone:
					m.turnEnded = true
				case protocol.StatusError:
					// Surface real session/turn errors in telemetry too (scrubbed) — otherwise they
					// were only visible in the local log, invisible to remote debugging.
					if t := m.hub.tel(); t != nil {
						t.Record("session.error", m.sess.Provider(), 0, fmt.Errorf("%s", ss.Detail))
					}
				}
				m.mu.Unlock()
				// Universal per-turn visibility: every provider funnels status through here, so ONE set
				// of log lines narrates every turn (start/end/error) in the daemon log + app log panel —
				// the fix for "0 daemon logs during a whole Q&A session". Errors are always logged.
				switch ss.Status {
				case protocol.StatusRunning:
					// opencode emits StatusRunning per TOOL CALL — logging each one flooded the log with
					// dozens of identical "turn start" lines per turn and buried real signals.
					if startedTurn {
						log.Printf("session %s (%s): turn start", m.sess.ID(), m.sess.Provider())
					}
				case protocol.StatusIdle, protocol.StatusDone:
					log.Printf("session %s (%s): turn end (%s)", m.sess.ID(), m.sess.Provider(), ss.Status)
					m.flushUI(ss.SessionID)    // emit any component/text left in an open fence, reset for next turn
					m.finalizeTurnTranscript() // persist the turn's assistant reply if the provider only streamed deltas
					// Record a "finished" activity item only when a real turn actually ran (saw deltas),
					// so idle re-attaches don't spam the feed.
					m.mu.Lock()
					ran := m.wasRunning
					m.mu.Unlock()
					if ran {
						m.hub.recordActivity(activity.Event{
							Kind: activity.KindFinished, SessionID: m.sess.ID(), Provider: m.sess.Provider(),
							Project: m.meta.cwd, Title: m.activityTitle() + " finished",
						})
					}
				case protocol.StatusError:
					log.Printf("session %s (%s): turn ERROR: %s", m.sess.ID(), m.sess.Provider(), ss.Detail)
					_ = m.hub.tr().Append(m.sess.ID(), transcript.Entry{Kind: "status", Text: "error", Detail: ss.Detail})
					m.hub.recordActivity(activity.Event{
						Kind: activity.KindError, SessionID: m.sess.ID(), Provider: m.sess.Provider(),
						Project: m.meta.cwd, Title: m.activityTitle() + " errored", Detail: ss.Detail, NeedsYou: true,
					})
				}
				m.onStatus(ss)
				m.turnOnStatus(ss) // Turn Engine: own status drives the turn state machine
			}
		}
		// Generative UI: scan assistant text for ```iron:ui``` fences. Complete, valid fences are
		// pulled OUT of the visible stream and re-emitted as normalized ui.component events; the rest
		// of the text streams normally. Invalid/unknown blocks stay inline as code (never broken).
		// This happens here, once, so every harness gets it for free.
		if ev.Type == protocol.TypeOutputDelta {
			// Only the PARENT session's own text goes through the fence segmenter — a sub-agent's
			// forwarded delta (SessionID == child id) must not be fed into the parent's segmenter.
			if d, ok := ev.Payload.(protocol.OutputDelta); ok && d.SessionID == m.sess.ID() {
				fwd, comps := m.seg.Feed(d.Text)
				m.emitUIComponents(d.SessionID, comps)
				if fwd == "" {
					continue // fully absorbed into a fence; nothing to stream this delta
				}
				d.Text = fwd
				ev.Payload = d
			}
		}
		// Generative UI in FINALIZED text: replayed history and resync messages arrive whole (never
		// through the delta segmenter), so their iron:ui payloads rendered as raw JSON forever.
		// Extract components here and strip them from the text, exactly like the streaming path.
		var extractedComps []protocol.UIComponent
		if ev.Type == protocol.TypeSessionMessage {
			if msg, ok := ev.Payload.(protocol.SessionMessage); ok && msg.SessionID == m.sess.ID() && msg.Role == "assistant" {
				if cleaned, comps := genui.Extract(msg.Text); len(comps) > 0 {
					msg.Text = cleaned
					ev.Payload = msg
					extractedComps = comps
				}
			}
		}
		raw, err := ev.Encode()
		if err != nil {
			continue
		}
		m.persistDurable(ev, raw)         // finalized messages / completed tools / error markers
		m.persistRenderable(ev.Type, raw) // sub-agent rows render as conversation and must survive too
		m.broadcast(raw)
		if len(extractedComps) > 0 {
			m.emitUIComponents(m.sess.ID(), extractedComps) // right after the cleaned message
		}
	}
	// The provider event stream ended. Distinguish an EXPLICIT user stop (drop the durable record)
	// from an UNEXPECTED provider exit — a crashed claude-code sidecar or an exited CLI process —
	// which must KEEP the record so the session resurfaces as stopped/restartable instead of
	// silently vanishing from every device (its transcript/resume data may still be recoverable).
	m.mu.Lock()
	stopped := m.userStopped
	m.mu.Unlock()
	// The provider stream ended with a turn still open → the turn can never complete. Close it as
	// abandoned so no client is left with an eternal spinner. (No-op if the turn already closed.)
	m.closeTurn("abandoned", "the agent's event stream ended")
	if stopped {
		m.hub.removeSession(m.sess.ID(), m)
	} else {
		m.hub.detachSession(m.sess.ID(), m)
	}
}

// broadcastUserEcho re-emits a user prompt to every subscriber tagged with who sent it, so a second
// device can tell your message apart from its own. The sending client already rendered it
// optimistically and dedups this echo by text.
func (m *managedSession) broadcastUserEcho(text, author string) {
	ev := agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
		SessionID: m.sess.ID(), Role: "user", Text: text, Author: author,
	}}
	if raw, err := ev.Encode(); err == nil {
		// Persist the user's half. The durable transcript used to hold only what the AGENT said, so a
		// restarted pi or CLI session came back showing answers to questions that had vanished.
		m.appendDurable(m.sess.ID(), "", raw)
		m.broadcast(raw)
	}
}

// appendDurable writes one frame to the durable transcript under the sequence lock.
func (m *managedSession) appendDurable(sid, msgID string, raw []byte) {
	db := m.hub.db
	if db == nil || m.meta.ephemeral {
		return
	}
	m.txMu.Lock()
	m.txSeq++
	seq := m.txSeq
	m.txMu.Unlock()
	if _, err := db.AppendTranscript(sid, seq, msgID, raw); err != nil {
		log.Printf("transcript: append %s failed: %v", sid, err)
	}
}

// persistRenderable stores a frame that RENDERS as conversation content but that the provider never
// finalizes into a message — generative-UI cards and sub-agent rows. Without these a restart leaves
// visible holes where the cards used to be.
func (m *managedSession) persistRenderable(typ string, raw []byte) {
	id := renderableID(typ, raw)
	if id == "" {
		return
	}
	m.appendDurable(m.sess.ID(), id, raw)
}

// renderableID derives a STABLE durable key for a frame that renders as conversation but that no
// provider finalizes into a message.
//
// Without one these rows were written with a NULL message id, which the store's unique index treats
// as "never a duplicate" — so every daemon restart re-derived the same generative-UI cards from the
// provider's re-streamed text and appended them AGAIN. Two restarts, two copies on screen. The
// payload ids are already stable (a card keeps its id as it goes running → ready), so keying on them
// makes the write idempotent and the re-stream harmless.
//
// Returns "" for frame types that must not be persisted at all.
func renderableID(typ string, raw []byte) string {
	var f struct {
		Payload struct {
			ID       string `json:"id"`
			ParentID string `json:"parent_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &f) != nil || f.Payload.ID == "" {
		return ""
	}
	switch typ {
	case protocol.TypeUIComponent:
		return "ui:" + f.Payload.ID
	case protocol.TypeSessionSubAgent:
		return "sub:" + f.Payload.ID
	}
	return ""
}

// encodeApprovalRequest frames an approval for broadcast (MCP approvals originate in the hub rather
// than arriving from a provider's event stream, so they need their own encoder).
func encodeApprovalRequest(ar protocol.ApprovalRequest) ([]byte, error) {
	return (agent.Event{Type: protocol.TypeApprovalRequest, Payload: ar}).Encode()
}

// todosChanged reports whether a to-do list differs from the previous one. Harnesses re-send the
// full list on every update, including when nothing actually moved.
func todosChanged(prev, next []protocol.Todo) bool {
	if len(prev) != len(next) {
		return true
	}
	for i := range next {
		if prev[i].Content != next[i].Content || prev[i].Status != next[i].Status {
			return true
		}
	}
	return false
}

// sendHistoryPage streams one page of older history to ONE subscriber, bracketed by begin/end so the
// client can place it above what it already has instead of appending it to the bottom.
//
// The frames go through that subscriber's own outbound channel — the same path the initial replay
// uses — so begin, the events, and end arrive in that order. Sending the bracket over the request
// socket while the events went through the subscriber queue would race.
func (m *managedSession) sendHistoryPage(conn *transport.Conn, loaded, limit int) {
	if limit <= 0 {
		limit = replayTailLimit
	}
	m.mu.Lock()
	sub := m.subs[conn]
	m.mu.Unlock()
	if sub == nil {
		return
	}
	page, more := m.historyPage(loaded, limit)
	sid := m.sess.ID()
	begin, err1 := (agent.Event{Type: protocol.TypeTranscriptPageBegin, Payload: protocol.TranscriptPageBegin{SessionID: sid}}).Encode()
	end, err2 := (agent.Event{Type: protocol.TypeTranscriptPageEnd, Payload: protocol.TranscriptPageEnd{SessionID: sid, Count: len(page), HasMore: more}}).Encode()
	if err1 != nil || err2 != nil {
		return
	}
	go func() {
		send := func(raw []byte) bool {
			select {
			case sub.ch <- raw:
				return true
			case <-sub.done:
				return false
			}
		}
		if !send(begin) {
			return
		}
		for _, raw := range page {
			if !send(raw) {
				return
			}
		}
		send(end)
	}()
}
