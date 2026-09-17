package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
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
// responseTimeout bounds how long a prompt may produce NOTHING before we tell the user it may not
// have arrived. It has to clear the slowest legitimate first-token latency, or it cries wolf.
//
// 25s did not. Extended thinking regularly runs past it before the first token, so a hard prompt
// produced "No response from the agent — your message may not have reached it" while the model was
// simply thinking. The banner then vanished when output arrived, but it had already been written to
// the durable transcript, so the conversation kept a permanent scary note about a turn that worked.
const responseTimeout = 90 * time.Second

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
		m.mu.Lock()
		lost := m.promptWriteAheadFailed
		m.mu.Unlock()
		detail := "No response from the agent — your message may not have reached it (a directory mismatch can accept the send but route it nowhere). Your prompt was saved; try “Recover session”, then resend."
		if lost {
			// Do not promise a recovery that cannot happen. If the write-ahead failed, the text exists
			// only in the client's own transcript, and "try Recover session" would send them looking
			// for something that was never written.
			detail = "No response from the agent — your message may not have reached it. It could NOT be saved to the daemon's transcript either, so copy your message before retrying."
		}
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

func (m *managedSession) disarmResponseWatchdog() {
	m.mu.Lock()
	m.awaitingResponse = false
	m.respWatchdogGen++
	m.mu.Unlock()
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

	mu              sync.Mutex
	subs            map[*transport.Conn]*subscriber
	transcript      [][]byte  // encoded protocol events, replayed to new subscribers
	transcriptBytes int       // running size of transcript (for the byte cap)
	ringSeq         uint64    // bumped on every ring write; half of the fullHistory cache key
	lastActivity    time.Time // last event time; surfaced as Session.UpdatedAt for sorting/relative time
	// pumpSeq counts events the pump has fully processed. See the bump site for why it exists.
	pumpSeq atomic.Uint64
	// segMu guards seg. The segmenter used to be touched only by the pump, so it needed no lock —
	// until the turn engine had to flush it as well (a reconciled turn never receives the provider
	// idle that the pump's flush hangs off).
	segMu sync.Mutex

	// pumpTasks lets another goroutine run work ON the pump, after everything already queued.
	//
	// This is a FENCE, and it replaced two failed attempts at timing. The reconciler asks the
	// provider to re-emit output it lost, which lands on the provider's channel and is drained by
	// the pump — so closing the turn from the turn-engine goroutine raced that content. Waiting for
	// a settled counter could not tell "the pump finished" from "the pump has not started"; adding
	// a parked flag fixed that but still stalled for the whole timeout whenever no pump was running.
	//
	// Posting the work to the pump removes the question. The pump drains provider events with
	// priority and runs a task only when that channel is momentarily empty, so a task posted after
	// Recover necessarily runs after the recovered frames have been broadcast. Buffered and posted
	// non-blockingly, so the turn engine can never block on the pump.
	pumpTasks chan func()
	// pumpAlive is true while the pump loop is running and therefore able to execute posted tasks.
	//
	// Without it, onPump succeeds against a buffered channel nobody is draining and the caller
	// believes its work is scheduled — so a reconciled turn would never close at all. Refusing when
	// no pump is running lets the caller do the work itself, out of order but done.
	pumpAlive atomic.Bool

	inTok, outTok int     // cumulative NEW tokens across the session (cache reads excluded)
	costUSD       float64 // cumulative cost (USD) — meaningful only when costKnown
	// costKnown is false until a provider actually reports a cost. Without it a session that was
	// never priced is indistinguishable from one that cost nothing, and the app rendered the former
	// as "$0.000".
	costKnown bool
	// contextTokens is the size of the conversation last sent to the model (cache read + that turn's
	// new input). REPLACED per turn, never summed — summing it is what reported 3.1M for a session
	// whose largest turn was 17k.
	contextTokens    int
	wasRunning       bool      // saw activity since the last idle (gates the "finished" push)
	turnStartedAt    time.Time // when the current turn started running (for the "finished" push duration)
	loopDoneNotified bool      // fired the "loop run finished" push once (loop sessions only)

	// Turn Engine state (see turn.go) — guarded by m.mu. turnPhase "" = no open turn.
	turnID         string
	turnPhase      string // running | awaiting_approval | stalled while open
	turnStarted    time.Time
	turnLastEvent  time.Time
	turnDetail     string
	turnKids       map[string]*protocol.TurnChild
	turnStopLoop   chan struct{}
	turnProbeFails int
	// turnProbeSince is when the CURRENT run of failed probes began (zero = the agent is answering).
	// Unreachability is judged on elapsed time, not on a count of attempts: counting made the verdict
	// depend on the tick rate, and at a 5s tick four failures declared an agent dead in twenty
	// seconds — less than a laptop takes to wake, a wifi handover to settle, or a busy opencode to
	// answer one slow request.
	turnProbeSince time.Time
	// turnRevives counts in-place repair attempts made for this outage, so we escalate through them
	// rather than retrying the same broken connection forever.
	turnRevives int

	// turnTools is the set of tool calls the provider has STARTED and not yet finished. A tool card
	// used to be fire-and-forget, so one whose completion event was lost span forever; knowing what is
	// outstanding is what lets closeTurn seal them.
	turnTools map[string]*protocol.TurnTool
	// promptWriteAheadFailed records that the last write-AHEAD of a user prompt did not reach disk.
	//
	// The write-ahead IS the "never lose work" guarantee: the prompt is written before it is sent, so
	// a send that vanishes can still be recovered. Its error was discarded — and the no-response
	// message then told the user "Your prompt was saved; try Recover session, then resend", which is
	// the single worst moment to be wrong about it. They act on that sentence.
	promptWriteAheadFailed bool
	// unknownToolStatus remembers which unrecognised tool statuses we have already complained about,
	// so the warning in turnOnTool fires once per word rather than once per frame.
	unknownToolStatus map[string]bool
	// turnToolAt is the last time any tool STARTED or FINISHED. It is the turn's real progress signal:
	// a provider can report "busy" indefinitely while wedged inside a single tool call (opencode's
	// probe reads an incomplete assistant message, which is exactly what a hung tool looks like), so
	// "busy" alone can never distinguish working from stuck. Movement here can.
	turnToolAt time.Time
	// turnStallReason is why we called this turn stalled, kept so the ~10s heartbeat can re-state it.
	// Heartbeats emit with an empty reason, which would otherwise blank the explanation seconds after
	// it appeared and leave a "stuck" chip with nothing behind it.
	turnStallReason string
	// turnNudges counts the nudges spent on the CURRENT turn (reset per turn, unlike the session-wide
	// heartbeat nudgeCount) — the budget before a stalled turn escalates to needs_you.
	turnNudges int
	// userInterrupted marks a turn the user themselves interrupted, so its close is reported as a
	// plain idle instead of paging them about an "error" they caused. Distinct from userStopped,
	// which means the whole SESSION is going away.
	userInterrupted bool

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
	noProgressFor  time.Duration // provider says busy but nothing progressed this long → stalled
	nudgeLimit     int           // nudges a stalled turn gets before escalating to needs_you
	unreachWindow  time.Duration // agent refusing connections this long → abandoned
	slowWindow     time.Duration // agent merely timing out this long → abandoned (far more generous)
	reviveLimit    int           // in-place repair attempts per outage

	txMu  sync.Mutex
	txSeq int64
	// transcriptTrimmed records that the in-memory ring has DROPPED events. Once true, the ring is no
	// longer a complete record of the session and must not be replayed as if it were.
	transcriptTrimmed bool
	// accMu guards asstAccum, asstPersisted and subAccum.
	//
	// These used to be pump-goroutine-only, unsynchronized. That invariant held exactly as long as the
	// turn was finalized only from the pump — and it no longer is: a turn now finalizes from whichever
	// path closes it, including the reconciler's direct close when the onPump queue is full. Rather
	// than make every future caller prove which goroutine it is on, the accumulator carries its own
	// lock. It is taken only on delta boundaries and at turn close, never around I/O.
	accMu         sync.Mutex
	asstAccum     strings.Builder
	asstPersisted bool
	// Per-sub-agent text for this turn, keyed by the child session id. Sub-agent output is streamed
	// as deltas and no provider ever finalizes it into a message, so without this a lane that read
	// correctly while it ran came back EMPTY after a restart — the lane announcement is durable, its
	// contents were not. Guarded by accMu, like asstAccum.
	subAccum map[string]*strings.Builder

	// ringFromStart reports whether m.transcript holds the session's history from its FIRST event.
	// False for any session this process ATTACHED to rather than created — a restored session's ring
	// starts empty and then fills with only what happens from now on, which is not the conversation.
	ringFromStart bool

	// Memoized fullHistory, for the path that has to merge the durable store into the ring.
	//
	// Assembling that costs a whole-transcript SQL read, a sha256 of every frame on both sides and a
	// JSON unmarshal of most of them. Subscribe paid it, and then every "show earlier messages" page
	// paid it AGAIN — so reading back through a long conversation re-derived the entire conversation
	// once per page, and switching between two restored sessions did it on every switch.
	//
	// Keyed by two monotonic counters, one per source: ringSeq moves on any ring write, txSeq on any
	// durable write. Deriving the key from the sources is the point. Hand-invalidating at each of the
	// call sites that write either one would be a single forgotten site away from replaying a
	// conversation that is missing its most recent message, which is the failure this file has already
	// shipped twice and commented at length about.
	//
	// Guarded by its own lock, taken only after m.mu is released, and released by the heartbeat sweep
	// so a session nobody is reading stops holding a second copy of its own transcript.
	histMu     sync.Mutex
	histCache  [][]byte
	histRing   uint64
	histTx     int64
	histAt     time.Time
	histBuilds atomic.Uint64 // rebuilds; read by the test that proves the memo is actually consulted

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
	// budgetStopped latches the money-ceiling stop for THIS turn. Deliberately separate from
	// hbState: that field is recomputed every tick from the same condition the stop is gated on, so
	// using it as the "already handled" guard made the guard answer its own question. Cleared by
	// openTurn so a raised budget re-arms.
	budgetStopped    bool
	maxNudges        int     // give-up bound (0 = default)
	budgetUSD        float64 // cost ceiling for autonomous nudging (0 = default)
	lastHandoffMtime int64   // mtime of the handoff file at last index (skip re-index if unchanged)
	model            string  // active model id ("" = provider default)
	modelProvider    string  // sub-provider/backend for the model
	pendingContext   string  // one-shot note prepended to the FIRST user prompt (multi-repo layout)

	seg genui.Segmenter // incremental scanner for ```iron:ui``` generative-UI fences in assistant text

	awaitingResponse bool                  // a prompt was sent and no event has come back yet (drives the no-response watchdog)
	respWatchdogGen  int                   // generation counter so a stale watchdog can't fire after a newer prompt/response
	userStopped      bool                  // the user explicitly stopped/removed this session (vs. an unexpected provider exit)
	conflicted       bool                  // this worktree session's branch would conflict with the default branch (passive badge)
	checkpoints      []protocol.Checkpoint // restore points snapshotted for this worktree session (newest last)

	// PR check watching (see prchecks.go), guarded by m.mu. prLastState is the last CI rollup this
	// daemon ACCOUNTED for — the thing that makes the "CI went red" push edge-triggered instead of
	// once per poll, so a flapping build or a re-run can't spam a phone. prNextPoll/prBackoff pace
	// the gh calls per session rather than globally, because a PR whose checks are still running
	// deserves a faster cadence than one that settled an hour ago.
	prLastState   string        // SUCCESS | FAILURE | PENDING | NONE; "" = never observed (adopt, don't announce)
	prNextPoll    time.Time     // earliest next gh call for this session
	prBackoff     time.Duration // current retry interval after polls gh could not answer (0 = none)
	prPolling     bool          // a poll is in flight; gh outlives a sweep tick, and two would race
	prWatchDone   bool          // PR merged/closed or worktree gone — stop polling this session for good
	prFingerprint string        // last broadcast rollup, so an unchanged poll wakes nobody

	// prPoll fetches this session's PR state. Nil means the real gh-backed worktree.PRState; tests
	// inject a scripted one. It lives on the session rather than in a package variable on purpose:
	// a package variable written by one test while another session's poll is still reading it is a
	// data race, and `go test -race` failing blocks every release.
	prPoll func(ctx context.Context, worktreePath, branch string) (worktree.PRInfo, error)
}

// markUserStopped records that the session's close is user-intended, so run()'s cleanup DELETES the
// durable record instead of preserving it. Without this, an unexpected provider exit (crashed
// claude-code sidecar / exited CLI) is indistinguishable from a stop and its record is wrongly dropped.
func (m *managedSession) markUserStopped() {
	m.mu.Lock()
	m.userStopped = true
	m.mu.Unlock()
}

// markUserInterrupted records that the CURRENT TURN is ending because the user interrupted it — as
// opposed to the session going away (markUserStopped) or the agent failing on its own.
//
// Without this, an interrupt was indistinguishable from a spontaneous death: publishVerdict saw a
// turn end in error or abandonment, filed a "stopped responding" item and sent a push. Pressing stop
// on your own agent and then being paged about it is the kind of thing that makes people stop
// trusting the notifications entirely. Cleared when the next turn opens.
func (m *managedSession) markUserInterrupted() {
	m.mu.Lock()
	m.userInterrupted = true
	m.mu.Unlock()
}

// activityTitle is a short human label for the session in the activity feed: the user-set name,
// else a repo/branch-ish hint from the cwd, else a short id.
// Takes m.mu: `meta.label` is MUTABLE — session.rename writes it, on the connection's read loop —
// while this is read from the event pump, the turn-engine goroutine and the PR-check poller. An
// unsynchronized string read against a concurrent write can hand back a mismatched pointer/length
// pair, so a rename landing at the moment a turn ends put a corrupted title into the activity feed
// and the needs-you inbox. Every caller holds no lock, so taking one here is safe.
func (m *managedSession) activityTitle() string {
	m.mu.Lock()
	defer m.mu.Unlock()
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

	// replayMu serialises the RE-subscribe replay (the session-switch path), which pushes a
	// transcript snapshot into s.ch from its own goroutine.
	//
	// Nothing stopped two of those goroutines existing at once: switch away from a session and back
	// while the previous replay is still draining into a slow socket — a phone over the relay — and
	// both push up to replayTailLimit frames into the same channel, interleaved with each other and
	// with live frames. The conversation then renders twice and shuffled: `running` tool cards
	// arriving after their `completed` twins, so finished tools revert to spinners, and deltas after
	// the message they belong to. That is the disorder joinHistory was written to prevent on the
	// other path; this one had no equivalent.
	replayMu sync.Mutex

	// KEPT, and the plan that said it would die was too broad about why it existed.
	//
	// It was doing TWO jobs. One was paging overlap — "is this live frame also in the replay I am
	// about to send" — and that is gone: every durable frame now carries its sequence and a page is
	// exactly the frames before a cursor, so there is nothing left to guess. The other is a provider
	// RE-STREAM: opencode and claude-code push their own history back through the pump on recover and
	// on a late attach. Those frames are deduplicated in the DATABASE by message id, but they are
	// still broadcast, and under the sequence change they arrive carrying a NEW number — so a cursor
	// cannot recognise them and the conversation would render twice. Different problem, same
	// symptom, and only the first one is solved by counting properly.
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

	// buffering holds live frames aside while subscribe assembles this subscriber's replay.
	//
	// The replay snapshot cannot be taken under m.mu — it reads the durable transcript out of SQLite
	// and hashes every frame — so there is a real window between "registered as a subscriber" and
	// "we know what the replay contains". A frame broadcast inside that window went out live AND
	// landed in the snapshot, and the dedup set did not exist yet to catch it, so the client rendered
	// it twice. Wide open for a restored session, which is exactly the case with the most history to
	// read. Frames that arrive while this is set are held here and appended after the replay, with
	// seen() dropping any the snapshot already contained.
	buffering bool
	buffered  [][]byte
}

// startBuffering diverts live frames until stopBuffering. Called with the session lock held, so no
// broadcast can slip between registration and this flag.
// Additive, NOT a reset. Two prepareSubscriptions can overlap for one (session, conn) —
// session.subscribe is dispatched inline, but session.create/attach/recover are async and also
// subscribe — and clearing here would drop whatever the first one was holding. Dropping frames is
// strictly worse than the duplicate this whole mechanism exists to prevent; the second drain simply
// returns nothing, and seen() dedupes anything the other replay already carried.
func (s *subscriber) startBuffering() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffering = true
}

// bufferFrame takes a live frame aside if a replay is being assembled, reporting whether it did.
func (s *subscriber) bufferFrame(raw []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.buffering {
		return false
	}
	s.buffered = append(s.buffered, raw)
	return true
}

// stopBuffering resumes live delivery and returns what arrived meanwhile. The flip and the drain are
// one critical section, so a concurrent bufferFrame either lands in the returned slice or goes out
// live — never into a slice nobody will read again.
func (s *subscriber) stopBuffering() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffering = false
	out := s.buffered
	s.buffered = nil
	return out
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
	// Hashed WITHOUT the sequence, for the same reason joinHistory is: a re-streamed frame is the
	// same frame and carries a different number. Comparing raw bytes made every re-stream a stranger
	// to the copy already delivered, which is the doubling this dedup exists to prevent.
	h := sha256.Sum256(protocol.StripSeq(raw))
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
		h := sha256.Sum256(protocol.StripSeq(f))
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
	// fanoutSynth marks the variant that COMBINED the others rather than attempting the task from
	// scratch. It competes in the same comparison and is kept the same way; the flag exists so the
	// UI can label it, since "this one read the others" is the single most useful thing to know
	// when judging its diff.
	fanoutSynth bool
	// fanoutSources are the 1-based variant numbers whose diffs the synthesis actually read. Some
	// may have been too large to include, and a synthesis of 2 of 6 attempts must not be presented
	// as though it weighed all six.
	fanoutSources []int
	// baseRefOverride pins a new worktree's starting commit instead of taking the repo's current
	// HEAD. Set for a synthesis variant, which is created long after its siblings and must branch
	// from the same place they did or its diff isn't comparable to theirs.
	baseRefOverride string

	// loopName is set when this session is a run of a recurring autonomous loop — used to fire the
	// "loop run finished" push once the run completes.
	loopName string

	// ephemeral: a scratch "just chat" session — no project, NOT persisted to the store.
	ephemeral bool

	// Where the agent process runs: execKind is "" for this Mac, protocol.ExecKindSSH for a remote
	// host, and execHost names that host. Held apart from label precisely because label is the
	// user's to rename — a renamed remote session must not become indistinguishable from a local one.
	execKind string
	execHost string
}

func newManagedSession(h *Hub, sess agent.Session, meta sessionMeta) *managedSession {
	now := time.Now()
	return &managedSession{hub: h, sess: sess, meta: meta, subs: map[*transport.Conn]*subscriber{},
		// Small buffer: the only poster is the reconciler, at most once per turn.
		pumpTasks:    make(chan func(), 4),
		lastActivity: now, createdAt: now,
		hbEvery: turnHeartbeatEvery, quietAfter: turnQuietAfter,
		reconcileTick: turnReconcileTick, probeFailLimit: turnProbeFailLimit,
		noProgressFor: turnNoProgressFor, nudgeLimit: turnNudgeLimit,
		unreachWindow: turnUnreachableWindow, slowWindow: turnSlowWindow, reviveLimit: turnReviveLimit}
}

// pushLabel is the session's name as a LOCK SCREEN has to read it: the user's label, and — when the
// agent isn't running on this Mac — the host it ran on. A push is the one place the session's own UI
// isn't there to say where the work happened, and the host used to reach the notification purely by
// accident: it was baked into the default label ("remote: build-box"), which session.rename replaces.
// A renamed remote session then pushed "deploy finished" with nothing to say which of five boxes
// deployed. host is empty for local sessions, so their notifications stay byte-identical.
func pushLabel(label, host string) string {
	if host == "" {
		return label
	}
	if label == "" {
		return host
	}
	return label + " on " + host
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
	label = pushLabel(label, m.meta.execHost)
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
		// A turn just ended, which is when a worktree session is most likely to have JUST opened its
		// PR (by `gh pr create` in the agent's own shell, which the daemon never sees). The PR watcher
		// backs off hard on polls gh can't answer, and "this branch has no PR yet" is one of those —
		// so without this reset a session that spends its first half hour PR-less would sit at the
		// half-hour ceiling and not notice the PR for that long. Re-arming it here costs at most one
		// extra gh call per finished turn. See prchecks.go.
		if finished && m.meta.worktreePath != "" && !m.prWatchDone {
			m.prBackoff, m.prNextPoll = 0, time.Time{}
		}
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
		if finished {
			// Retire the loop run, or the scheduler's concurrency gate never reopens and the loop
			// never fires again. Unconditional: it no-ops for a session no loop started.
			m.hub.retireLoopRun(m.sess.ID(), "done")
		}
	case protocol.StatusError:
		m.wasRunning = false
		// Captured under the lock: resolveFanout clears meta.fanoutGroup from another goroutine, and
		// a torn string header here faults inside checkFanoutDone's map hash — which Go does not
		// make recoverable, so it takes the whole daemon with it.
		errGroup := m.meta.fanoutGroup
		m.mu.Unlock()
		m.hub.pushAgentError(m.sess.ID(), label, ss.Detail)
		// A failed variant still ENDED, so the group may now be complete. Without this the fan-out
		// notification waited on a session that was never going to report idle.
		if g := errGroup; g != "" {
			m.hub.checkFanoutDone(g)
		}
		// A failed run is a FINISHED run as far as the loop scheduler is concerned; leaving it
		// "running" wedges the loop exactly as an unretired success does.
		m.hub.retireLoopRun(m.sess.ID(), "error")
	default:
		m.mu.Unlock()
	}
}

// lastActive is the unix time of the session's last event — the liveness clock the DB TTL
// prunes against (so a session with no activity for the TTL window ages out of the store).
// snapshotMeta returns a COPY of this session's metadata, taken under m.mu.
//
// meta is not immutable: session.rename writes meta.label on the connection's read loop, and
// resolving a fan-out clears meta.fanoutGroup — both under m.mu. Several readers took only h.mu,
// which is a different lock and therefore no mutual exclusion at all. Copying a Go string header
// non-atomically can pair one value's data pointer with another's length, so `strings.TrimSpace` on
// the result faults; watchPreviewPorts runs that read every four seconds from a goroutine started
// with no recover(), which makes a rename landing on the wrong tick fatal to the whole daemon.
//
// h.mu → m.mu is the order the rest of the file already uses (sessionList, usageReport, the
// heartbeat sweep), so callers holding h.mu may call this directly.
func (m *managedSession) snapshotMeta() sessionMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meta
}

// lastActiveAt is lastActive as a time.Time, for callers comparing two sessions rather than
// reporting an epoch on the wire.
func (m *managedSession) lastActiveAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastActivity
}

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
	// The WHOLE struct, under the lock. meta.label and meta.fanoutGroup are both written after
	// construction (session.rename on a connection's read loop, resolveFanout on another), so reading
	// any of it unlocked races; copying one field at a time just invites the next one to be missed.
	meta := m.meta
	label := meta.label
	inTok, outTok, cost := m.inTok, m.outTok, m.costUSD
	ctxTok, costKnown := m.contextTokens, m.costKnown
	isWorkspace := len(meta.members) > 0
	status := m.lastStatus
	turnOpen := m.turnPhase != ""
	model, modelProvider := m.model, m.modelProvider
	mode := m.mode
	conflicted := m.conflicted
	m.mu.Unlock()
	if status == "" {
		// No status event yet, so ask the turn engine instead of assuming. "Freshly created" used to
		// mean running unconditionally, which is right only when the session was created WITH a
		// prompt — and the New Session sheet explicitly allows starting without one. A session that
		// has never been asked to do anything then reported "working…" forever, with no turn, no
		// messages, and nothing that would ever clear it.
		if turnOpen {
			status = protocol.StatusRunning
		} else {
			status = protocol.StatusIdle
		}
	}
	return protocol.Session{
		ID:            m.sess.ID(),
		Provider:      m.sess.Provider(),
		Status:        status, // real last status (idle/error/awaiting_approval), not a hardcoded "running"
		Name:          label,
		ProjectID:     meta.projectID,
		Cwd:           meta.cwd,
		WorkspaceName: meta.workspaceName,
		Branch:        meta.branch,
		IsWorkspace:   isWorkspace,
		ParentID:      meta.parentID,
		Subtask:       meta.subtask,
		Port:          meta.port,
		PreviewURL:    m.hub.preview.URL(m.sess.ID()),
		IssueKey:      meta.issueKey,
		IssueID:       meta.issueID,
		Model:         model,
		ModelProvider: modelProvider,
		Mode:          mode,
		UpdatedAt:     updated,
		InputTokens:   inTok,
		OutputTokens:  outTok,
		CostUSD:       cost,
		ContextTokens: ctxTok,
		CostKnown:     costKnown,
		Conflicted:    conflicted,
		FanoutGroup:   meta.fanoutGroup,
		FanoutVariant: meta.fanoutVariant,
		Ephemeral:     meta.ephemeral,
		ExecKind:      meta.execKind,
		ExecHost:      meta.execHost,
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
	// Hashed WITHOUT the sequence. A frame's seq is where it landed, not what it says: the same
	// message re-streamed after a restart is the same message and carries a different number, and the
	// durable copy is stamped while a provider's fresh re-stream is not. Comparing the raw bytes made
	// every re-streamed frame a stranger to its own durable twin, so the conversation rendered twice.
	ringAt := make(map[string][]int, len(ring))
	for i, r := range ring {
		h := sha256.Sum256(protocol.StripSeq(r))
		k := string(h[:])
		ringAt[k] = append(ringAt[k], i)
	}
	type placed struct{ pos, seq int }
	// pos = ring index this durable frame maps to; -1 means "older than the ring".
	mapped := make([]placed, len(durable))
	for i, d := range durable {
		h := sha256.Sum256(protocol.StripSeq(d))
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
	s, replay, already := m.prepareSubscription(conn)
	if already {
		// The client RE-subscribed to a session it already had a subscription for — this is a session
		// SWITCH (the app cleared its transcript view on open, or switched away and back). Previously we
		// returned early here with NO replay, so the pane stayed blank and "the data never reloaded".
		// Re-send the transcript to the SAME subscriber (its writeLoop drains s.ch) so the conversation
		// repopulates. Runs in a goroutine so a full outbound buffer can't stall the event pump.
		go func() {
			// One replay at a time for this subscriber. A second switch waits for the first to
			// finish rather than interleaving with it.
			s.replayMu.Lock()
			defer s.replayMu.Unlock()
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

// prepareSubscription registers conn as a subscriber (if it is not one already) and assembles every
// frame it is owed: the replay snapshot, the current turn state, and anything broadcast while that
// snapshot was being read.
//
// Split out from subscribe because the window between registering and snapshotting is the whole
// defect — a frame broadcast inside it went out live AND landed in the snapshot — and this is the
// seam a test can hold both halves of at once. Delivery differs between a new and a re-subscribing
// client; what they are owed does not.
func (m *managedSession) prepareSubscription(conn *transport.Conn) (*subscriber, [][]byte, bool) {
	m.mu.Lock()
	existing, already := m.subs[conn]
	s := existing
	if !already {
		s = &subscriber{conn: conn, ch: make(chan []byte, outboundBuffer), done: make(chan struct{})}
		m.subs[conn] = s
	}
	// Hold live frames aside until the replay is known. Set under m.mu — the same lock broadcast
	// takes to snapshot the subscriber list — so there is no gap between being a subscriber and
	// being one whose live traffic is accounted for.
	s.startBuffering()
	m.mu.Unlock()
	// Whatever happens below, this subscriber must not be left muted: on the normal path
	// stopBuffering has already run and this returns nothing.
	defer func() {
		for _, raw := range s.stopBuffering() {
			select {
			case s.ch <- raw:
			default:
			}
		}
	}()
	replay := m.replayFrames()
	// A self-replaying provider pushes its history through broadcast AFTER this snapshot; suppress
	// the repeat rather than doubling the conversation on screen.
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
	for _, raw := range s.stopBuffering() {
		if s.seen(raw) {
			continue
		}
		replay = append(replay, raw)
	}
	return s, replay, already
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
	// Tear the connection down too, for the same reason dropClient does.
	//
	// A severed per-session subscription is INVISIBLE to the client: its socket is still healthy,
	// hub-level frames still arrive, and nothing re-subscribes it because from its side nothing
	// happened. The turn heartbeat does stop — but the client's only staleness detector is
	// `self.busy && ...`, so it cannot fire for a session with no turn in flight. Someone reading a
	// finished session, or one that goes idle a moment later, simply watches it stop updating forever.
	// The reconnect path is the only thing that can actually re-subscribe them.
	//
	// Asynchronous because this runs from the broadcast path and dropClient takes the hub lock; the
	// teardown is not ordered with respect to anything here.
	if m.hub != nil && s.conn != nil {
		go m.hub.dropClient(s.conn)
	}
}

// emitUIComponents broadcasts each generative-UI component the segmenter produced as its own
// ui.component event (stamping the session id). Called from the event pump on assistant deltas.
func (m *managedSession) emitUIComponents(sessionID string, comps []protocol.UIComponent) {
	for _, c := range comps {
		c.SessionID = sessionID
		if raw, err := (agent.Event{Type: protocol.TypeUIComponent, Payload: c}).Encode(); err == nil {
			m.broadcast(m.persistRenderable(protocol.TypeUIComponent, raw))
		}
	}
}

// flushUI finalizes the generative-UI segmenter at turn end: it emits any component/text held in a
// closing fence and resets the segmenter for the next turn. Called on idle/done.
func (m *managedSession) flushUI(sessionID string) {
	m.segMu.Lock()
	fwd, comps := m.seg.Flush()
	m.seg = genui.Segmenter{}
	m.segMu.Unlock()
	m.emitUIComponents(sessionID, comps)
	if fwd != "" {
		if raw, err := (agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: sessionID, Text: fwd}}).Encode(); err == nil {
			// Accumulated as well as broadcast. The segmenter holds a line until it is
			// newline-terminated, and models routinely end a reply without a trailing newline — so
			// for the delta-only providers this residual IS the last line of most replies. It went
			// out live but bypassed persistDurable, so the synthetic end-of-turn message written a
			// moment later was missing its final line: the transcript on screen and the transcript on
			// disk disagreed about where the answer ended, and only the stored one was truncated.
			//
			if sessionID == m.sess.ID() {
				m.accMu.Lock()
				m.asstAccum.WriteString(fwd)
				m.accMu.Unlock()
			}
			m.broadcast(raw)
		}
	}
}

// surfaceApproval puts an approval in front of the user: enriched with the scopes an ALWAYS could
// narrow to, recorded, counted, and broadcast hub-wide as well as to this session's subscribers.
//
// Extracted so the auto-answer paths have somewhere to FALL BACK to. Those paths answer on the user's
// behalf and suppress the card, and they used to discard the provider's error — so a Respond that
// failed left the harness waiting on a decision it never received, for an approval no human was ever
// shown. The turn hung with nothing on screen to act on.
func (m *managedSession) surfaceApproval(ar *protocol.ApprovalRequest) {
	// Offer the scopes an ALWAYS could narrow to. Computed here, once, from what this
	// harness actually told us, so the client never has to parse a command itself.
	m.mu.Lock()
	projectID := m.meta.projectID
	m.mu.Unlock()
	ar.SuggestedScopes = suggestScopes(*ar, ar.Patterns, projectID)
	// The caller must take its event payload AFTER this returns: ar is a value, so a payload
	// assigned before this line captures a copy with no scopes on it.
	m.hub.recordApproval(*ar, m)
	m.mu.Lock()
	m.pendingApprovals++
	m.mu.Unlock()
	// Tell EVERY client, not just this session's subscribers.
	//
	// A client subscribes to a session when it opens it, so a request raised by a session
	// nobody is looking at reached nobody — and that is precisely the session you need to
	// be told about. The Fleet renders approval controls per card so you can answer
	// without opening anything; it could never show them for an unopened session, which
	// is backwards, since triaging agents you are NOT watching is what the fleet is for.
	//
	// Observed: three fan-out agents sat blocked on a Write for thirteen minutes while
	// their cards read "On track". Opening one made its approval appear instantly; the
	// other two stayed silent.
	//
	// The asymmetry is what gives it away — RESOLUTION was already broadcast hub-wide
	// (TypeApprovalResolved via h.broadcast), so clients were told an approval had been
	// ANSWERED while never being told it had been ASKED.
	//
	// Subscribers of this session are SKIPPED: they already receive it through the
	// per-session fan-out below, and delivering twice is not harmless. A client that
	// merely stores the event is idempotent, but one that ACTS on it acts twice — the
	// full-stack E2E test answers each approval it sees, and a duplicate made it respond
	// twice, the second failing with "no such approval" about one run in three.
	m.mu.Lock()
	subscribed := make(map[*transport.Conn]bool, len(m.subs))
	for c := range m.subs {
		subscribed[c] = true
	}
	m.mu.Unlock()
	m.hub.broadcastExcept(protocol.TypeApprovalRequest, *ar, subscribed)
	m.hub.pushApproval(*ar)
}

// autoAnswer responds to an approval on the user's behalf, and surfaces it if that fails.
//
// Respond reaches the harness over a real transport (opencode POSTs; a non-2xx is an error), so it
// can fail — and every caller here suppresses the approval card on the assumption that it won't. The
// answer stays asynchronous because Respond can block and the pump must not stall, but a failure now
// falls back to asking the human instead of vanishing.
func (m *managedSession) autoAnswer(ar protocol.ApprovalRequest, decision, note string) {
	go func() {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = m.sess.Respond(ctx, ar.ApprovalID, decision)
			cancel()
			if err == nil {
				if note != "" {
					m.emitTool(note)
				}
				return
			}
		}
		log.Printf("session %s: could not auto-%s %s (%v) — surfacing it instead, or the turn waits "+
			"forever on a decision nobody was asked for", m.sess.ID(), decision, ar.Tool, err)
		m.surfaceApproval(&ar)
	}()
}

// noteWriteAhead records whether a user prompt reached the durable transcript before it was sent.
//
// A failure here is not fatal to the turn — the prompt still goes to the provider — but it voids the
// "never lose work" guarantee, and the daemon must stop claiming otherwise. Logged loudly because an
// operator can act on it (a full disk, a bad permission on ~/.oculus/transcripts) and nothing else
// would ever mention it.
func (m *managedSession) noteWriteAhead(err error) {
	m.mu.Lock()
	m.promptWriteAheadFailed = err != nil
	m.mu.Unlock()
	if err != nil {
		log.Printf("session %s: WRITE-AHEAD FAILED for a user prompt: %v — the text is not in the "+
			"durable transcript, so it cannot be recovered if the send is lost", m.sess.ID(), err)
	}
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
// Returns the frame to BROADCAST: the same bytes it stored, sequence included, or raw unchanged for
// anything it did not store.
func (m *managedSession) persistDurable(ev agent.Event, raw []byte) []byte {
	db := m.hub.db
	if db == nil || m.meta.ephemeral {
		return raw // ephemeral scratch chats aren't persisted (keeps "no sessions row" == orphan for prune)
	}
	sid := m.sess.ID()
	var msgID string
	switch ev.Type {
	case protocol.TypeSessionMessage:
		msg, ok := ev.Payload.(protocol.SessionMessage)
		if !ok || msg.SessionID != sid {
			return raw
		}
		msgID = msg.MsgID
		if msg.Role == "assistant" {
			m.accMu.Lock()
			m.asstPersisted = true // a real assistant message → skip the synthetic delta one at turn end
			m.accMu.Unlock()
		}
	case protocol.TypeSessionTool:
		t, ok := ev.Payload.(protocol.SessionTool)
		if !ok || t.SessionID == "" || (t.Status != "completed" && t.Status != "error") {
			return raw // only the final tool state is durable
		}
		// Child-addressed cards are durable too. The guard used to be `t.SessionID != sid`, which
		// dropped every sub-agent's tool cards on the floor: opencode's lanes rendered them live and
		// replayed with only their text, and once claude-code started addressing its cards to the
		// lane it would have lost them the same way. A lane that shows its work while you watch and
		// forgets it on reload is the same "no output" complaint one reconnect later.
		//
		// Keyed per lane so a child's card cannot collide with a parent card that shares its id —
		// the store's unique index would otherwise treat the second one as a duplicate and drop it.
		msgID = "tool:" + t.ID
		if t.SessionID != sid {
			msgID = "tool:" + t.SessionID + ":" + t.ID
		}
	case protocol.TypeSessionStatus:
		ss, ok := ev.Payload.(protocol.SessionStatus)
		if !ok || ss.SessionID != sid || ss.Status != protocol.StatusError {
			return raw
		}
		// error marker: NULL id (each distinct)
	case protocol.TypeOutputDelta:
		if d, ok := ev.Payload.(protocol.OutputDelta); ok {
			m.accMu.Lock()
			if d.SessionID == sid {
				m.asstAccum.WriteString(d.Text) // accumulate the VISIBLE (post-fence) streamed text
			} else if d.SessionID != "" {
				// A sub-agent's stream. Accumulated per lane so the turn can finalize each one.
				if m.subAccum == nil {
					m.subAccum = map[string]*strings.Builder{}
				}
				b := m.subAccum[d.SessionID]
				if b == nil {
					b = &strings.Builder{}
					m.subAccum[d.SessionID] = b
				}
				b.WriteString(d.Text)
			}
			m.accMu.Unlock()
		}
		return raw
	default:
		return raw
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
	return m.appendDurable(sid, msgID, raw)
}

// finalizeTurnTranscript runs on idle: if the turn streamed assistant text but no finalized assistant
// SessionMessage was persisted (claude-code/pi/cli stream deltas only, never a finalized message),
// persist a synthetic one so the durable transcript actually contains the reply. Resets per-turn
// state. NULL msg id — those providers never re-stream history, so there's nothing to dedup against.
func (m *managedSession) finalizeTurnTranscript() {
	db := m.hub.db
	// Take the whole turn's accumulation and CLEAR it under one lock, then do the durable writes
	// outside it. Clearing here rather than at the end is what makes this safe to call from every
	// close path: a second call finds an empty accumulator and does nothing.
	m.accMu.Lock()
	text := m.asstAccum.String()
	persisted := m.asstPersisted
	subs := m.subAccum
	m.asstAccum.Reset()
	m.asstPersisted = false
	m.subAccum = nil
	m.accMu.Unlock()

	// Mirror the reply into the write-ahead JSONL.
	//
	// That package's doc has always said it "mirrors assistant/tool/status events". It never did:
	// the only things ever appended were user prompts and error statuses. So the never-lose-work
	// backstop held exactly one half of every conversation — every question, no answers — and the
	// CLI recap built from it handed an amnesiac agent a list of the user's own questions with
	// nothing between them.
	//
	// Deliberately OUTSIDE the `db != nil` gate below: the JSONL matters most precisely when SQLite
	// is unavailable, which is the one case the old placement skipped. And deliberately not gated on
	// `persisted` either — a provider that finalizes its own message writes that to SQLite only, so
	// gating here would keep the backstop empty for exactly the providers that work best.
	if strings.TrimSpace(text) != "" {
		if tr := m.hub.tr(); tr != nil {
			_ = tr.Append(m.sess.ID(), transcript.Entry{Kind: "assistant", Text: text})
		}
	}

	if db != nil && !persisted && strings.TrimSpace(text) != "" {
		ev := agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: m.sess.ID(), Role: "assistant", Text: text}}
		if raw, err := ev.Encode(); err == nil {
			// RING it, don't deliver it. Writing only to SQLite created a frame that existed in one
			// source and not the other, and merging the two rendered every reply twice — so it has to
			// reach the ring. But it must NOT reach the clients currently watching, because this frame
			// is nothing but the deltas they were just sent, concatenated.
			//
			// Sending it live doubled a reply on screen whenever the streamed text was not the last
			// row at turn end. The client only replaces a finalized message onto a still-STREAMING
			// assistant row, and a generative-UI component seals that row and appends its own — so a
			// turn that ended with an iron:ui block rendered prose, card, then the same prose again.
			// Tool cards seal the row the same way. On REPLAY this frame is deduplicated against the
			// text the deltas rebuild, which is exactly why the fault only ever showed up live.
			raw = m.appendDurable(m.sess.ID(), "", raw)
			m.recordOnly(raw)
		}
	}
	// Same treatment for every sub-agent that spoke this turn: one finalized message per lane, so
	// the lane still has its report after a restart. Ringed rather than delivered, for the reason
	// recordOnly exists — live watchers already received these as deltas.
	for child, b := range subs {
		text := b.String()
		if db == nil || strings.TrimSpace(text) == "" {
			continue
		}
		ev := agent.Event{Type: protocol.TypeSessionMessage,
			Payload: protocol.SessionMessage{SessionID: child, Role: "assistant", Text: text}}
		if raw, err := ev.Encode(); err == nil {
			raw = m.appendDurable(m.sess.ID(), "sub-msg:"+child, raw)
			m.recordOnly(raw)
		}
	}
}

// recordOnly appends an event to the replayable ring WITHOUT delivering it to current subscribers.
//
// The complement of broadcastTransient, which delivers without recording. Both exist because
// "record" and "deliver" are separate questions and broadcast answers them together: use this for a
// frame that belongs in a later attach's history but restates something live watchers already have.
func (m *managedSession) recordOnly(raw []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transcript = append(m.transcript, raw)
	m.transcriptBytes += len(raw)
	m.ringSeq++
	m.lastActivity = time.Now()
	m.trimTranscript()
}

// broadcast records the event and enqueues it to every current subscriber without
// blocking: a subscriber whose bounded queue is full is dropped rather than allowed to
// stall the run() goroutine that pumps the provider's event stream.
func (m *managedSession) broadcast(raw []byte) {
	m.mu.Lock()
	m.transcript = append(m.transcript, raw)
	m.transcriptBytes += len(raw)
	m.ringSeq++
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
		if s.bufferFrame(raw) {
			continue // mid-subscribe: held aside and appended after the replay, deduped against it
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
	ringSeq := m.ringSeq
	m.mu.Unlock()
	if fromStart && !trimmed {
		return ring // this process saw the session from its first event: the ring is the whole story
	}
	db := m.hub.db
	if db == nil {
		return ring
	}
	m.txMu.Lock()
	txSeq := m.txSeq
	m.txMu.Unlock()

	if cached := m.cachedHistory(ringSeq, txSeq); cached != nil {
		return cached
	}
	m.histBuilds.Add(1)
	durable, err := db.Transcript(m.sess.ID())
	if err != nil || len(durable) == 0 {
		return ring
	}
	joined := joinHistory(durable, ring)
	m.rememberHistory(joined, ringSeq, txSeq)
	return append([][]byte(nil), joined...)
}

// histCacheTTL is how long an unread memo survives the heartbeat sweep. Long enough to cover a
// subscribe and the burst of history pages that follows it; short enough that a session the user
// opened once and left is not still holding a second copy of its transcript a minute later.
const histCacheTTL = 30 * time.Second

// cachedHistory returns the memo if it was built from exactly this pair of source versions.
// The returned slice is a fresh header over shared frames: callers sub-slice and append to what
// fullHistory hands back, and appending into spare capacity would otherwise overwrite the memo.
func (m *managedSession) cachedHistory(ringSeq uint64, txSeq int64) [][]byte {
	m.histMu.Lock()
	defer m.histMu.Unlock()
	if m.histCache == nil || m.histRing != ringSeq || m.histTx != txSeq {
		return nil
	}
	m.histAt = time.Now()
	return append([][]byte(nil), m.histCache...)
}

func (m *managedSession) rememberHistory(joined [][]byte, ringSeq uint64, txSeq int64) {
	m.histMu.Lock()
	defer m.histMu.Unlock()
	m.histCache, m.histRing, m.histTx, m.histAt = joined, ringSeq, txSeq, time.Now()
}

// expireHistoryCache drops a memo nobody has read for histCacheTTL. Called from the heartbeat tick,
// which is the only thing that visits every session on a timer.
func (m *managedSession) expireHistoryCache(now time.Time) {
	m.histMu.Lock()
	defer m.histMu.Unlock()
	if m.histCache != nil && now.Sub(m.histAt) > histCacheTTL {
		m.histCache = nil
	}
}

// historyPage returns the events immediately BEFORE the newest `loaded` ones, oldest-first, plus
// whether anything older still remains.
// historyPageBefore returns the frames immediately before beforeSeq — the cursor-based pager.
//
// The count-based historyPage below computes `len(all) - loaded` and therefore requires the client's
// tally to agree exactly with the daemon's ring. It never reliably did: the client had to guess, by
// message type, which frames the daemon stored, and every over-count asks for a page that starts
// before the transcript actually ends, leaving a hole the client cannot see. Here the cursor is a
// number the daemon itself put on a frame, so there is nothing to agree about.
//
// Unsequenced frames (streaming deltas, transient status) ride along inside the range but never
// serve as the boundary: they are superseded by the finalized, sequenced frame and must not move a
// cursor they have no position in.
func (m *managedSession) historyPageBefore(beforeSeq int64, limit int) (page [][]byte, more bool) {
	all := m.fullHistory()
	end := len(all)
	for i, raw := range all {
		if s := frameSeq(raw); s > 0 && s >= beforeSeq {
			end = i
			break
		}
	}
	// Walk back `limit` SEQUENCED frames, keeping everything in between.
	start, kept := end, 0
	for start > 0 && kept < limit {
		start--
		if frameSeq(all[start]) > 0 {
			kept++
		}
	}
	page = all[start:end]
	if start > 0 {
		return page, true
	}
	// Reached the front of the live window; the rest is archived, not gone.
	if kept >= limit {
		return page, m.hasArchived()
	}
	older, err := m.archivedBefore(limit - kept)
	if err != nil || len(older) == 0 {
		return page, false
	}
	return append(older, page...), true
}

// frameSeq reads a frame's durable sequence, or 0 for one that has none.
func frameSeq(raw []byte) int64 {
	var f struct {
		Seq int64 `json:"seq"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return 0
	}
	return f.Seq
}

func (m *managedSession) historyPage(loaded, limit int) (page [][]byte, more bool) {
	all := m.fullHistory()
	end := len(all) - loaded
	if end > 0 {
		start := end - limit
		if start < 0 {
			start = 0
		}
		if start > 0 {
			return all[start:end], true
		}
		// Reached the start of what's live. Keep going into the archive below rather than reporting
		// "no more" — the rest of the conversation is compressed, not gone.
		page = all[start:end]
		limit -= len(page)
		if limit <= 0 {
			return page, m.hasArchived()
		}
	}
	// Past the live window: page back through the compressed chunks. This is the half that makes a
	// long session's beginning reachable again — it used to have been deleted outright.
	older, err := m.archivedBefore(limit)
	if err != nil || len(older) == 0 {
		return page, false
	}
	return append(older, page...), true
}

// archivedBefore fetches the newest `limit` events that sit BEFORE everything currently live.
func (m *managedSession) archivedBefore(limit int) ([][]byte, error) {
	db := m.hub.db
	if db == nil {
		return nil, nil
	}
	var oldestLive int64
	if err := db.OldestTranscriptSeq(m.sess.ID(), &oldestLive); err != nil {
		return nil, err
	}
	return db.ArchivedBefore(m.sess.ID(), oldestLive, limit)
}

// hasArchived reports whether any compressed history exists before the live window.
func (m *managedSession) hasArchived() bool {
	db := m.hub.db
	if db == nil {
		return false
	}
	st, err := db.ArchiveStatsFor(m.sess.ID())
	return err == nil && st.ArchivedRows > 0
}

// trimTranscript enforces the retention cap (by event count and total bytes), dropping
// the oldest events. Caller must hold m.mu.
func (m *managedSession) trimTranscript() {
	for len(m.transcript) > 0 && (len(m.transcript) > maxTranscriptEvents || m.transcriptBytes > maxTranscriptBytes) {
		m.transcriptBytes -= len(m.transcript[0])
		m.transcript[0] = nil // release the backing bytes for GC
		m.transcript = m.transcript[1:]
		m.ringSeq++                // dropping the front changes the history as much as appending to the back
		m.transcriptTrimmed = true // the ring is now a WINDOW, not the whole session
	}
}

// run pumps the session's events until it ends: records approval ownership + pushes,
// then broadcasts every event to all subscribers.
func (m *managedSession) run() {
	// Contain a panic to THIS session.
	//
	// A panic in any goroutine takes the whole process with it, and this one runs per session,
	// parsing whatever a third-party harness chose to send. So a single malformed frame from one
	// provider could kill the daemon — every other session dies with it, the app shows a dead
	// connection with no reason, and the only evidence is a stack trace in a log file the user has
	// never heard of. The blast radius should be the session that caused it.
	//
	// The session is then reported as errored rather than left silently stopped, because a turn that
	// is never going to produce another event must not render as "working" forever.
	// Captured ONCE, up front. The recover handler must not call back into the session: it is
	// running because that session just demonstrated it can panic, and a panic inside a deferred
	// recover takes the process down anyway — defeating the whole guard.
	sid, provider := m.sess.ID(), m.sess.Provider()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		log.Printf("session %s (%s): PANIC in the event pump: %v\n%s", sid, provider, r, debug.Stack())
		if t := m.hub.tel(); t != nil {
			t.Record("session.panic", provider, 0, fmt.Errorf("%v", r))
		}
		detail := "This session stopped unexpectedly (internal error). Its transcript is saved; start a new session to carry on."
		_ = m.hub.tr().Append(sid, transcript.Entry{Kind: "status", Text: "error", Detail: detail})
		ss := protocol.SessionStatus{SessionID: sid, Status: protocol.StatusError, Detail: detail}
		if raw, err := (agent.Event{Type: protocol.TypeSessionStatus, Payload: ss}).Encode(); err == nil {
			m.broadcast(raw)
		}
		m.closeTurn(protocol.StatusError, "panic in the event pump")
		// And tear the session DOWN, the same way the normal stream-ended path below does.
		//
		// Recovering used to return straight out of run(), skipping every line after the pump loop —
		// so the session stayed in h.sessions with a dead pump behind it. Nothing was listening on the
		// provider any more, but the hub still held the binding: its pending approvals could never be
		// resolved (the answer goes through this pump), its MCP token stayed valid, and every session
		// list kept offering a row that answers nothing. detachSession, not removeSession: a panic is
		// an unexpected end, not a user stop, so the durable record must survive for the session to
		// resurface as stopped and restartable.
		//
		// Under its OWN recover. This whole handler runs because something already proved it can
		// panic, and a panic inside a deferred recover ends the process — which is precisely the
		// outcome the containment exists to prevent. Leaving the session bound is bad; taking every
		// other session down with it is worse, so the teardown is allowed to fail loudly and alone.
		func() {
			defer func() {
				if r2 := recover(); r2 != nil {
					log.Printf("session %s (%s): PANIC while tearing down after a pump panic: %v", sid, provider, r2)
				}
			}()
			m.hub.detachSession(sid, m)
		}()
	}()
	// A worktree session is the only kind that can have a PR, so it is the only kind whose CI is
	// worth watching. Starting the hub's watcher from here (rather than from main) means a daemon
	// that never opens a worktree never runs the ticker — and because ensurePRWatch is idempotent
	// per hub, N worktree sessions still produce ONE poller, not one goroutine each. Sessions
	// restored after a restart come through here too, so a PR opened before the restart is picked
	// back up.
	if m.meta.worktreePath != "" && m.meta.branch != "" {
		m.hub.ensurePRWatch()
	}
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
	// Explicit receive rather than `for range`, so the pump can also run posted tasks — and run them
	// only once the provider's own events are drained. See pumpTasks.
	events := m.sess.Events()
	m.pumpAlive.Store(true)
	defer func() {
		m.pumpAlive.Store(false)
		// Run whatever was posted just as the loop was ending, so a task is never silently stranded
		// by the race between posting and exit.
		for {
			select {
			case fn := <-m.pumpTasks:
				if fn != nil {
					fn()
				}
			default:
				return
			}
		}
	}()
	for {
		var ev agent.Event
		var ok bool
		select {
		case ev, ok = <-events:
		default:
			// Nothing waiting from the provider: now a posted task may run, in the order it was
			// posted relative to the frames above.
			select {
			case ev, ok = <-events:
			case fn := <-m.pumpTasks:
				if fn != nil {
					fn()
				}
				continue
			}
		}
		if !ok {
			break
		}
		m.noteTurnEvent() // every provider event = liveness for the Turn Engine
		if sa, ok := ev.Payload.(protocol.SubAgent); ok && ev.Type == protocol.TypeSessionSubAgent && sa.ParentID == m.sess.ID() {
			m.turnOnChild(sa) // sub-agents are children of the turn
		}
		// Tool calls are turn state too: what's outstanding (so it can be sealed) and when anything
		// last moved (so a wedged turn is distinguishable from a slow one).
		if t, ok := ev.Payload.(protocol.SessionTool); ok && ev.Type == protocol.TypeSessionTool && t.SessionID == m.sess.ID() {
			m.turnOnTool(t)
		}
		// Anything tagged with a DIFFERENT session id came from a sub-agent — credit that child's own
		// liveness clock, not just the parent's.
		if childID := eventSessionID(ev); childID != "" && childID != m.sess.ID() {
			m.turnOnChildEvent(childID)
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
					m.autoAnswer(ar, protocol.DecisionDeny, "⊘ "+ar.Tool+" blocked — "+mode+" mode is read-only")
					continue
				}
				// Repository metadata is refused before ANY rule is consulted, and before the user is
				// asked. Ordering matters twice over: a standing "always allow write" must not reach
				// .git/hooks, and this is deliberately not a question to put on a card, because the
				// consequence is invisible when asked and arrives later from our own git commands.
				// See guardApproval.
				if g := guardApproval(ar); g.reason != "" {
					log.Printf("session %s: denied %s — %s", m.sess.ID(), ar.Tool, g.reason)
					m.autoAnswer(ar, protocol.DecisionDeny, "⊘ "+ar.Tool+" blocked — "+g.reason)
					continue
				}
				// YOLO answers everything itself — but only from HERE, after guardApproval.
				//
				// The user asked not to be prompted; they did not ask to disable the guard on
				// repository metadata. That guard exists for consequences that are invisible at the
				// moment of asking and arrive later from our own git commands (a written .git/hooks
				// script runs on the next commit), so "I approved it" is not informed consent for it.
				// Keeping the guard ahead of yolo means yolo cannot be used, deliberately or by an
				// injected instruction, to install a persistence mechanism silently — and the block
				// is still surfaced as a visible tool note rather than a silent refusal.
				if modeAutoApproves(m.sessionMode()) {
					m.autoAnswer(ar, protocol.DecisionAllow, "")
					continue
				}
				// A persisted rule answers it silently — permissions are asked ONCE, ever, not once
				// per session. The request never reaches a client.
				if m.hub.autoAllowApproval(m, ar) {
					continue
				}
				// Enrich FIRST, then take the payload. `ar` is a value, so assigning it to the
				// event boxes a COPY — doing that before surfaceApproval adds SuggestedScopes left
				// the subscribed clients (the ones actually showing the chat) with an empty scope
				// list, while the hub-wide broadcast that deliberately skips them carried the full
				// one. The client falls back to a single "Always allow <tool> everywhere" button
				// when scopes are empty, so the narrow choices the daemon had just computed were
				// unreachable from the only surface the user was looking at.
				m.surfaceApproval(&ar)
				ev.Payload = ar
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
				if u.CostReported {
					m.costUSD += u.CostUSD
					m.costKnown = true
				}
				// Replace, don't accumulate — this is how big the conversation is, not what it spent.
				if ctx := u.CacheReadTokens + u.InputTokens; ctx > 0 {
					m.contextTokens = ctx
				}
				m.mu.Unlock()
				// The context meter lives in the status bar, which facts drive.
				m.hub.broadcastFacts(m)
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
				recordError := false // telemetry is sent after the unlock; see below
				switch ss.Status {
				case protocol.StatusRunning:
					startedTurn = m.turnEnded || !m.wasRunning
					m.turnEnded = false
				case protocol.StatusIdle, protocol.StatusDone:
					m.turnEnded = true
				case protocol.StatusError:
					// Recorded AFTER the unlock. See below — reaching for the hub from under this
					// lock is what deadlocks the daemon.
					recordError = true
				}
				m.mu.Unlock()
				// Surface real session/turn errors in telemetry too (scrubbed) — otherwise they are
				// only visible in the local log, invisible to remote debugging.
				//
				// Outside m.mu, and that placement is the whole point. tel() takes the HUB lock, and
				// the hub takes locks in the other order: Hub.sessionList holds h.mu and calls
				// m.info(), which wants m.mu. Doing this under m.mu meant an ordinary failed turn —
				// a rate limit, a model error — raced every session.list, and session.list is
				// re-broadcast on create/rename/delete and requested by every client on connect.
				// Losing that race deadlocks h.mu, which every dispatch case, every broadcast and
				// every subscribe takes: all sessions freeze, every device goes dead holding a
				// healthy socket, and only killing the daemon recovers it.
				if recordError {
					if t := m.hub.tel(); t != nil {
						t.Record("session.error", m.sess.Provider(), 0, fmt.Errorf("%s", ss.Detail))
					}
				}
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
				m.segMu.Lock()
				fwd, comps := m.seg.Feed(d.Text)
				m.segMu.Unlock()
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
		// Broadcast what was STORED, sequence and all: the client's paging cursor is that number,
		// so the live frame and the frame a later page returns have to be the same bytes.
		raw = m.persistDurable(ev, raw)
		raw = m.persistRenderable(ev.Type, raw) // sub-agent rows render as conversation and must survive too
		m.broadcast(raw)
		if len(extractedComps) > 0 {
			m.emitUIComponents(m.sess.ID(), extractedComps) // right after the cleaned message
		}
		// A watermark of "the pump has finished with this event".
		//
		// Bumped LAST, after everything the event causes, so an observer that sees it advance knows
		// the frame is on the subscriber queue. The turn engine uses it to avoid closing a turn on
		// top of content it just asked the provider to re-emit (see waitForPumpQuiet).
		//
		// Not turnLastEvent: that is only bumped for specific event kinds, so it cannot answer
		// "has the pump drained".
		m.pumpSeq.Add(1)
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

// recordUserMessage persists the user's half of the conversation and, when echo is set, sends it to
// this session's subscribers.
//
// The two are separate because only the ECHO depends on knowing who sent it — an unattributed echo
// would render a second copy on the device that just sent the prompt. This used to be one function
// called only when the author was known, which quietly made the durable record conditional on
// attribution: a client that never identified itself had its prompts persisted nowhere, and a
// restarted pi or CLI session came back showing answers to questions that had vanished. That is the
// exact symptom the durable user-half was added to fix, still present for anyone the daemon could
// not name. Persistence is about what was said; attribution is about who said it.
func (m *managedSession) recordUserMessage(text, author string, echo bool) {
	ev := agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
		SessionID: m.sess.ID(), Role: "user", Text: text, Author: author,
	}}
	raw, err := ev.Encode()
	if err != nil {
		return
	}
	// Broadcast the STORED copy: its sequence is what the client pages from, and an echo without one
	// would leave the user's own message re-delivered by the next page.
	raw = m.appendDurable(m.sess.ID(), "", raw)
	if echo {
		m.broadcast(raw)
	}
}

// appendDurable writes one frame to the durable transcript under the sequence lock.
// advanceDurable rewrites an already-stored renderable in place — a card moving running → done.
// Uses a fresh seq only for the row that does not exist yet; an existing row keeps its position.
func (m *managedSession) advanceDurable(sid, msgID string, raw []byte) {
	db := m.hub.db
	if db == nil || m.meta.ephemeral || msgID == "" {
		return
	}
	m.txMu.Lock()
	m.txSeq++
	seq := m.txSeq
	m.txMu.Unlock()
	if err := db.UpsertRenderable(sid, seq, msgID, raw); err != nil {
		log.Printf("transcript: advance %s failed: %v", sid, err)
	}
}

// It returns the frame with its sequence STAMPED IN — the copy the caller must broadcast, so the
// bytes a client receives live and the bytes a later page returns are the same. Returns raw
// unchanged when nothing was stored (no db, or an ephemeral session), which is also the honest
// answer: an unpersisted frame has no cursor position and must not claim one.
func (m *managedSession) appendDurable(sid, msgID string, raw []byte) []byte {
	db := m.hub.db
	if db == nil || m.meta.ephemeral {
		return raw
	}
	m.txMu.Lock()
	m.txSeq++
	seq := m.txSeq
	m.txMu.Unlock()
	stamped := protocol.StampSeq(raw, seq)
	if _, err := db.AppendTranscript(sid, seq, msgID, stamped); err != nil {
		log.Printf("transcript: append %s failed: %v", sid, err)
	}
	return stamped
}

// persistRenderable stores a frame that RENDERS as conversation content but that the provider never
// finalizes into a message — generative-UI cards and sub-agent rows. Without these a restart leaves
// visible holes where the cards used to be.
// Returns the frame to broadcast — stamped with its sequence when it was stored, unchanged when it
// was not. Disjoint from persistDurable by type (that one takes messages and tools, this one UI
// components and sub-agent rows), so exactly one of the pair ever assigns a given frame's sequence.
func (m *managedSession) persistRenderable(typ string, raw []byte) []byte {
	id := renderableID(typ, raw)
	if id == "" {
		return raw
	}
	return m.appendDurable(m.sess.ID(), id, raw)
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
func (m *managedSession) sendHistoryPage(conn *transport.Conn, beforeSeq int64, loaded, limit int) {
	if limit <= 0 {
		limit = replayTailLimit
	}
	m.mu.Lock()
	sub := m.subs[conn]
	m.mu.Unlock()
	if sub == nil {
		return
	}
	var page [][]byte
	var more bool
	if beforeSeq > 0 {
		page, more = m.historyPageBefore(beforeSeq, limit)
	} else {
		page, more = m.historyPage(loaded, limit)
	}
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

// onPump runs fn on the pump goroutine, after everything the provider has already sent.
//
// Non-blocking by construction: if the queue is full the task is dropped and reported false, because
// the turn engine must never wait on the pump — the pump can block on a database append, and a turn
// loop stuck behind it would freeze the session it is supposed to be supervising.
func (m *managedSession) onPump(fn func()) bool {
	if !m.pumpAlive.Load() {
		return false // nobody would ever run it
	}
	select {
	case m.pumpTasks <- fn:
		return true
	default:
		return false
	}
}
