package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// The Turn Engine: the daemon-owned truth about whether a session's turn is alive (see
// docs/turn-engine-plan.md). The client renders `turn.state` verbatim and runs NO liveness timers —
// "abandoned" (the only state that renders as "no response") can only be declared here, backed by a
// provider probe, never by a timer guess.
//
// Timings are package vars so tests can shrink them (fake-clock-free, still fast).
var (
	turnHeartbeatEvery = 10 * time.Second // emit turn.state at least this often while a turn is open
	turnQuietAfter     = 30 * time.Second // no provider events for this long → start probing
	turnReconcileTick  = 5 * time.Second  // reconciler poll granularity
	turnProbeFailLimit = 4                // consecutive probe FAILURES (not "busy") before abandoning
	// turnNoProgressFor is how long a turn may claim to be busy while NOTHING actually progresses
	// before we call it stalled.
	//
	// Four minutes was too tight and produced false stalls on ordinary work: a full `pnpm test`, a
	// browser suite, a large build and a slow inference call all routinely exceed it without a
	// single tool boundary in between. The cost of being wrong is asymmetric — a nudge lands as a
	// user message in someone's conversation, and a false page teaches people to ignore real ones —
	// so this errs long. A genuinely wedged turn takes longer to report; that is the better trade.
	turnNoProgressFor = 10 * time.Minute
	// turnNudgeLimit is how many nudges a stalled turn gets before we stop guessing and page a human.
	turnNudgeLimit = 3
	// turnUnreachableWindow is how long an agent must be unreachable — REFUSING connections, not
	// merely slow — before we call the turn dead. Judged in elapsed time rather than failed attempts
	// so the verdict can't ride on the tick rate: the old count-based rule declared an agent dead
	// after four failures, which at a 5s tick is twenty seconds, less than a MacBook takes to wake.
	turnUnreachableWindow = 2 * time.Minute
	// turnSlowWindow is the same budget for an agent that is merely SLOW (timeouts rather than
	// refusals). It is far more generous because a timeout is not evidence of absence — a long
	// session's own history read can outrun a probe deadline while the agent works perfectly.
	turnSlowWindow = 10 * time.Minute
	// turnReviveLimit bounds in-place repair attempts per outage.
	turnReviveLimit = 3
)

// openTurn starts (or refreshes) the session's turn. Called on prompt-send and on the provider's own
// StatusRunning. Idempotent while a turn is open.
func (m *managedSession) openTurn(detail string) {
	m.mu.Lock()
	if m.turnPhase != "" { // already open — just refresh
		if detail != "" {
			m.turnDetail = detail
		}
		m.turnLastEvent = time.Now()
		m.mu.Unlock()
		return
	}
	m.turnID = randToken()
	m.turnPhase = protocol.StatusRunning
	m.turnStarted = time.Now()
	m.turnLastEvent = m.turnStarted
	m.turnToolAt = m.turnStarted // a fresh turn has, by definition, just progressed
	m.turnDetail = detail
	m.turnKids = map[string]*protocol.TurnChild{}
	m.turnTools = map[string]*protocol.TurnTool{}
	m.turnProbeFails = 0
	m.turnNudges = 0
	m.userInterrupted = false
	// Clear the OUTAGE state too, not just the counters.
	//
	// These are per-outage, and an outage cannot outlive the turn it happened in. Left set, a new
	// turn inherits an outage clock from an old one: the first transient probe hiccup then computes
	// `down` from the PREVIOUS turn's timestamp, blows straight past both windows, and abandons a
	// brand-new turn instantly while reporting an absurd duration ("unreachable for 30m30s" on a
	// turn thirty seconds old). Same class as the accumulating clock in noteTurnEvent — the reset
	// was applied there and missed here.
	m.turnProbeSince = time.Time{}
	m.turnRevives = 0
	stop := make(chan struct{})
	m.turnStopLoop = stop
	m.mu.Unlock()
	// Keep the machine awake for the life of the turn. A Mac that sleeps thirty seconds after you
	// walk away stops the agent mid-thought and lets the relay registration go stale, so the phone
	// finds a session that is neither running nor reachable — the exact failure "continue from
	// anywhere" exists to prevent.
	if m.hub != nil && m.hub.awake != nil {
		m.hub.awake.Hold()
	}
	m.emitTurn("")
	// A turn EDGE is the only moment a session's rendered state changes, so it is the only moment
	// worth telling every client about. Clients that aren't subscribed to this session (the sidebar,
	// the fleet grid, the phone that just connected) otherwise hold whatever session.list said when
	// they last asked — a frozen snapshot that goes stale the instant work starts.
	m.publishSessionState(protocol.StatusRunning, detail)
	// A session that is working is not waiting on you: a NEW turn means the previous ask was answered
	// or the previous error was recovered from. (openTurn is idempotent while a turn is open, so an
	// approval raised mid-turn can't be cleared by its own turn.)
	m.clearNeedsYou()
	go m.turnLoops(stop)
}

// noteTurnEvent marks provider liveness — called for EVERY event the provider produces (own or
// sub-agent), so `last_event_at` is honest and the reconciler only probes on true quiet.
func (m *managedSession) noteTurnEvent() {
	m.mu.Lock()
	if m.turnPhase != "" {
		m.turnLastEvent = time.Now()
		m.turnProbeFails = 0
		// An event is the STRONGEST possible proof of reachability — stronger than any probe, because
		// the agent just spoke to us. So it ends the outage outright.
		//
		// Leaving this set was a real bug: the clock started at the first failed probe and was cleared
		// only by a successful revive, so intermittent probe failures across a long, busy turn
		// accumulated for the life of the turn. A turn that streamed tool results for ten minutes was
		// abandoned as "unreachable for 10m10s" — a duration measured from its first hiccup rather
		// than from any continuous outage, while the agent was demonstrably working the whole time.
		m.turnProbeSince = time.Time{}
		m.turnRevives = 0
	}
	m.mu.Unlock()
}

// turnOnStatus folds the session's OWN status events into the turn state machine.
func (m *managedSession) turnOnStatus(ss protocol.SessionStatus) {
	switch ss.Status {
	case protocol.StatusRunning:
		m.mu.Lock()
		open := m.turnPhase != ""
		// A turn we had written off as stalled reporting running again is a RECOVERY — it deserves the
		// same visible edge as an answered approval, or the client keeps rendering "stuck, nudging"
		// over an agent that is demonstrably working.
		changed := m.turnPhase == protocol.StatusAwaitingApproval || m.turnPhase == protocol.StatusStalled
		if open {
			m.turnPhase = protocol.StatusRunning
			if ss.Detail != "" {
				m.turnDetail = ss.Detail
			}
		}
		m.mu.Unlock()
		if !open {
			m.openTurn(ss.Detail)
		} else if changed {
			m.emitTurn("") // approval answered → visibly back to running
			m.publishSessionState(protocol.StatusRunning, ss.Detail)
			// The agent resumed, so the question it was blocked on has been answered — on THIS device
			// or another one. Retire the inbox item now rather than making the user dismiss, on every
			// device, a request that no longer exists.
			m.clearNeedsYou()
		}
	case protocol.StatusAwaitingApproval:
		m.mu.Lock()
		open := m.turnPhase != ""
		if open {
			m.turnPhase = protocol.StatusAwaitingApproval
		}
		m.mu.Unlock()
		if open {
			m.emitTurn("")
			m.publishSessionState(protocol.StatusAwaitingApproval, "")
		}
	case protocol.StatusIdle, protocol.StatusDone:
		m.closeTurnFrom(protocol.StatusIdle, "", true)
	case protocol.StatusError:
		m.closeTurnFrom(protocol.StatusError, ss.Detail, true)
	}
}

// turnOnChild folds a sub-agent lifecycle event into the turn's children.
func (m *managedSession) turnOnChild(sa protocol.SubAgent) {
	state := protocol.SubAgentRunning
	switch sa.Status {
	case protocol.SubAgentDone:
		state = protocol.SubAgentDone
	case protocol.SubAgentError:
		state = protocol.SubAgentError
	}
	now := time.Now()
	m.mu.Lock()
	if m.turnPhase == "" {
		m.mu.Unlock()
		return
	}
	if m.turnKids == nil {
		m.turnKids = map[string]*protocol.TurnChild{}
	}
	kid := m.turnKids[sa.ID]
	if kid == nil {
		kid = &protocol.TurnChild{ID: sa.ID, StartedAt: now.Unix()}
		m.turnKids[sa.ID] = kid
	}
	kid.State = state
	kid.LastEventAt = now.Unix()
	if sa.Title != "" {
		kid.Title = sa.Title
	}
	m.turnLastEvent = now
	// A child starting or finishing is real forward motion for the PARENT turn, the same way a tool
	// starting or finishing is — it is the delegation equivalent of a tool call.
	m.turnToolAt = now
	m.mu.Unlock()
	m.emitTurn("")
}

// turnOnChildEvent records that a child produced ANY event (output, a tool call of its own), keeping
// that child's own liveness clock honest. The parent's clock is bumped by every event from every
// child, so it says nothing about whether a PARTICULAR child is still alive: this is what makes
// "child 2 of 3 stalled" observable instead of invisible behind a chatty sibling.
func (m *managedSession) turnOnChildEvent(childID string) {
	now := time.Now()
	m.mu.Lock()
	if m.turnPhase == "" || m.turnKids == nil {
		m.mu.Unlock()
		return
	}
	if kid := m.turnKids[childID]; kid != nil {
		kid.LastEventAt = now.Unix()
	}
	// A child producing ANYTHING is forward motion for the parent turn.
	//
	// Without this the parent's progress clock froze the moment its sub-agents were spawned — the
	// only later stamps are the parent's own tool boundaries and child START/FINISH — so a fan-out
	// whose children ran a ten-minute test suite was declared "stalled (no progress for 4m0s)" and
	// nudged, while every child was working and streaming output the whole time. Delegated work is
	// still work; the parent looking idle is what delegation LOOKS like.
	m.turnToolAt = now
	m.mu.Unlock()
}

// eventSessionID pulls the session id off the event payloads that carry real work. It covers the
// types a sub-agent actually streams (text, deltas, tools, status) rather than every payload in the
// protocol: this feeds a liveness clock, so missing an exotic type costs nothing, and the default of
// "" simply means "no child credited".
func eventSessionID(ev agent.Event) string {
	switch p := ev.Payload.(type) {
	case protocol.SessionMessage:
		return p.SessionID
	case protocol.OutputDelta:
		return p.SessionID
	case protocol.SessionTool:
		return p.SessionID
	case protocol.SessionStatus:
		return p.SessionID
	default:
		return ""
	}
}

// turnOnTool folds a tool call into the turn: a `running` tool joins the outstanding set, a finished
// one leaves it. Either way it stamps turnToolAt — the turn's progress signal, which is the only
// thing that can tell a busy-and-working turn from a busy-and-wedged one.
func (m *managedSession) turnOnTool(t protocol.SessionTool) {
	now := time.Now()
	m.mu.Lock()
	if m.turnPhase == "" {
		m.mu.Unlock()
		return
	}
	if m.turnTools == nil {
		m.turnTools = map[string]*protocol.TurnTool{}
	}
	switch {
	case protocol.IsToolFinished(t.Status):
		delete(m.turnTools, t.ID)
	default: // running (or any status a provider invents): it is outstanding until proven otherwise
		// Say so when the word is not one we know. Treating an unrecognised status as "still running"
		// is the safe default, but it is SILENT — and a provider that reports a finished tool with the
		// wrong word therefore looks exactly like a tool that never finished. The turn then seals the
		// card as an error at close, writing the seal note over a result that had arrived perfectly
		// well. The AG-UI adapter said "done" (a TURN status) instead of "completed" and did this to
		// every successful tool call it ever made; nothing anywhere reported it. Once per session per
		// word, so a genuinely chatty provider cannot flood the log.
		if t.Status != "" && t.Status != protocol.ToolRunning {
			if m.unknownToolStatus == nil {
				m.unknownToolStatus = map[string]bool{}
			}
			if !m.unknownToolStatus[t.Status] {
				m.unknownToolStatus[t.Status] = true
				log.Printf("turn: session %s (%s): unknown tool status %q — treating the tool as still "+
					"running; a finished tool must report \"completed\" or \"error\"",
					m.sess.ID(), m.sess.Provider(), t.Status)
			}
		}
		tt := m.turnTools[t.ID]
		if tt == nil {
			tt = &protocol.TurnTool{ID: t.ID, StartedAt: now.Unix()}
			m.turnTools[t.ID] = tt
		}
		if t.Name != "" {
			tt.Name = t.Name
		}
		if t.Title != "" {
			tt.Title = t.Title
		}
	}
	m.turnLastEvent = now
	m.turnToolAt = now
	m.mu.Unlock()
}

// closeTurn ends the turn in a terminal state (idle | error | abandoned) and stops its loops.
// Idempotent: only the first close wins. This is the DAEMON's own verdict path (the reconciler, a
// dead event stream) — see closeTurnFrom for why that distinction matters.
func (m *managedSession) closeTurn(state, reason string) {
	m.closeTurnFrom(state, reason, false)
}

// closeTurnFrom is closeTurn with the source of the close. providerDriven means the provider itself
// reported the terminal status, so the event pump has ALREADY written lastStatus, recorded the
// finished/error activity item and fired its push — publishing again would double every one of them.
// Every other close (probe-abandonment, stream death, reconciled idle) is the daemon's own
// conclusion, and nothing else in the system knows about it: that is the path that used to end
// silently, leaving a dead agent rendering as "running" forever with no feed entry and no push.
func (m *managedSession) closeTurnFrom(state, reason string, providerDriven bool) {
	m.mu.Lock()
	if m.turnPhase == "" {
		m.mu.Unlock()
		return
	}
	m.turnPhase = ""
	stop := m.turnStopLoop
	m.turnStopLoop = nil
	// Persist the turn's streamed reply HERE, on the one path every terminal state passes through,
	// rather than only on the provider's idle. finalizeTurnTranscript used to hang off the pump's
	// idle/done branch alone, and it is also the only thing that clears the accumulator — so a turn
	// that ended any other way (RUN_ERROR, a dead stream, probe abandonment, a reconciled close, an
	// unanswered approval) lost its reply from the durable transcript AND left it in the accumulator,
	// where it was prepended to the NEXT turn's synthetic message. The previous turn's answer then
	// reappeared on screen below a later prompt, attributed to the wrong turn. That hit every
	// delta-only provider — claude-code, pi, cli and now AG-UI, which no longer finalizes a message
	// of its own. The call is idempotent, so the pump's existing one on the idle path is harmless.
	if m.hub != nil && m.hub.awake != nil {
		defer m.hub.awake.Release() // balanced with the Hold in openTurn
	}
	// Seal the children. The provider-level seal (opencode marks its kids done when ITS session.idle
	// arrives) only covers the clean path — a turn ended by the reconciler, by abandonment, or by
	// stream loss never got that event, and a fan-out's sub-agent cards then spun forever with no way
	// to recover short of restarting the app. The parent's turn being over means no child can still
	// be running, whatever state we last heard for it; this is the one choke point every close path
	// shares, so the invariant lives here.
	sealed := protocol.SubAgentDone
	if state == protocol.StatusError || state == protocol.StatusAbandoned || state == protocol.StatusNeedsYou {
		sealed = protocol.SubAgentError // don't dress a dead turn's children as cleanly finished
	}
	var toSeal []protocol.SubAgent
	for _, k := range m.turnKids {
		if !protocol.IsSubAgentFinished(k.State) {
			k.State = sealed
			toSeal = append(toSeal, protocol.SubAgent{ParentID: m.sess.ID(), ID: k.ID, Status: sealed})
		}
	}
	// Seal the outstanding TOOL calls for the same reason, and it is the same bug: a tool card is only
	// ever resolved by its own completion event, so a turn that ended without one left the card
	// spinning forever — the "my glob has been running for six hours" report. The turn being over is
	// proof the tool is not still running, whatever we last heard about it.
	var toolSeal []protocol.SessionTool
	for _, t := range m.turnTools {
		toolSeal = append(toolSeal, protocol.SessionTool{
			SessionID: m.sess.ID(), ID: t.ID, Name: t.Name, Title: t.Title,
			Status: protocol.ToolError, Output: toolSealNote(state, reason),
		})
	}
	m.turnTools = nil
	m.mu.Unlock()
	// flushUI first: the segmenter holds a line until it is newline-terminated, and that residual IS
	// the last line of most replies. It writes into the accumulator, so it has to land before the
	// accumulator is drained.
	m.flushUI(m.sess.ID())
	m.finalizeTurnTranscript()
	if stop != nil {
		close(stop)
	}
	// Broadcast the seals as ordinary sub-agent events so every subscriber's per-card state resolves —
	// the terminal turn snapshot alone doesn't reach the inline cards, which key off these.
	for _, sa := range toSeal {
		if raw, err := (agent.Event{Type: protocol.TypeSessionSubAgent, Payload: sa}).Encode(); err == nil {
			m.broadcast(raw)
			// ADVANCE the stored row rather than appending. The pump wrote this lane as "running"
			// under the same stable id, and an INSERT OR IGNORE seal is silently dropped — so the
			// lane replayed in the state it started in and spun forever for a sub-agent that had
			// already finished.
			m.advanceDurable(m.sess.ID(), "sub:"+sa.ID, raw)
		}
	}
	if len(toSeal) > 0 {
		log.Printf("turn: session %s sealed %d sub-agent(s) as %s on %s close", m.sess.ID(), len(toSeal), sealed, state)
	}
	for _, st := range toolSeal {
		if raw, err := (agent.Event{Type: protocol.TypeSessionTool, Payload: st}).Encode(); err == nil {
			m.broadcast(raw)
			// The seal is the card's only terminal state, and persistDurable stores exactly that —
			// it was simply never called here, so an interrupted turn's cards came back on reload
			// still spinning.
			m.persistDurable(agent.Event{Type: protocol.TypeSessionTool, Payload: st}, raw)
		}
	}
	if len(toolSeal) > 0 {
		log.Printf("turn: session %s sealed %d unfinished tool call(s) on %s close", m.sess.ID(), len(toolSeal), state)
	}
	m.emitTurn2(state, reason)
	m.publishVerdict(state, reason, providerDriven)
	if state == protocol.StatusError || state == protocol.StatusAbandoned {
		log.Printf("turn: session %s closed %s: %s", m.sess.ID(), state, reason)
	}
}

// publishVerdict makes the end of a turn visible OUTSIDE the session's own subscribers. turn.state is
// transient and session-scoped; without this a turn the daemon ended by itself changed nothing a
// user can see: session.list kept serving "running" (lastStatus was never written), the Activity
// feed had no entry, and no push went out — an agent could die on a sleeping Mac and every surface
// still showed it working.
func (m *managedSession) publishVerdict(state, reason string, providerDriven bool) {
	if m.hub == nil {
		return
	}
	if providerDriven {
		// The pump owns the status for these (it distinguishes idle from done, which we'd flatten);
		// only the cross-client state edge is missing, so publish just that.
		m.mu.Lock()
		status := m.lastStatus
		m.mu.Unlock()
		// Carry the REASON. Publishing an empty detail here while the pump broadcasts the same status
		// WITH its detail put two error frames on the wire through two different queues — a hub-wide
		// broadcast and the session's own — and the client, which dedups only against the immediately
		// preceding row, rendered both: a generic "the agent reported an error" followed by the real
		// one. Same text, one bubble.
		m.publishSessionState(status, reason)
		return
	}
	failed := state == protocol.StatusError || state == protocol.StatusAbandoned
	stuck := state == protocol.StatusNeedsYou
	status := protocol.StatusIdle
	switch {
	case failed:
		status = protocol.StatusError
	case stuck:
		status = protocol.StatusNeedsYou
	}
	m.mu.Lock()
	// A turn open when the user pressed Stop ends as "abandoned" through the stream-death path. That
	// is the human's own doing: record the state, but don't page them about an error they caused. The
	// same goes for an interrupt — which used to page anyway, because only Stop set a flag and
	// interrupt reached this code looking exactly like a spontaneous agent failure.
	if m.userStopped || m.userInterrupted {
		failed, stuck, status = false, false, protocol.StatusIdle
	}
	m.lastStatus = status
	m.turnEnded = true
	// ran is the pump's "a real turn was in flight" flag, and its gate for the finished activity
	// item. Claiming it here means whichever of us closes the turn first records exactly one item.
	ran := m.wasRunning
	m.wasRunning = false
	label := m.meta.label
	if label == "" {
		label = m.meta.workspaceName
	}
	project := m.meta.cwd
	stopped := m.userStopped || m.userInterrupted
	m.mu.Unlock()

	// Recorded: this is the daemon ending the turn on its own, so nothing else puts it in the ring.
	m.publishStateRecorded(status, reason)
	if stopped {
		return
	}
	if stuck {
		// Deliberately NOT KindError. The agent didn't fail — it stopped moving, we asked it to
		// continue as many times as we're willing to, and now it's a person's call. Framing that as an
		// error trained everyone to ignore the one signal that actually needs them.
		m.hub.recordActivity(activity.Event{
			Kind: activity.KindStalled, SessionID: m.sess.ID(), Provider: m.sess.Provider(),
			Project: project, Title: m.activityTitle() + " is stuck", Detail: reason,
			NeedsYou: true,
		})
		m.hub.pushAgentStalled(m.sess.ID(), label, reason)
		return
	}
	if failed {
		detail := reason
		if detail == "" {
			detail = "the agent stopped responding"
		}
		m.hub.recordActivity(activity.Event{
			Kind: activity.KindError, SessionID: m.sess.ID(), Provider: m.sess.Provider(),
			Project: project, Title: m.activityTitle() + " stopped responding", Detail: detail,
			NeedsYou: true, // nobody else will chase this: it needs a human
		})
		m.hub.pushAgentError(m.sess.ID(), label, detail)
		return
	}
	// A daemon-reconciled idle (we recovered a completion event the provider lost) is a normal finish
	// from the user's point of view — it belongs in the feed like any other completed turn.
	if !ran {
		return // no work actually ran (or the pump already logged the finish): don't invent feed noise
	}
	m.hub.recordActivity(activity.Event{
		Kind: activity.KindFinished, SessionID: m.sess.ID(), Provider: m.sess.Provider(),
		Project: project, Title: m.activityTitle() + " finished", Detail: reason,
	})
}

// publishSessionState broadcasts one compact per-session state to EVERY connected client on a turn
// edge, so clients can fold live state into their session list instead of rendering the frozen
// session.list snapshot they fetched on connect. Subscribers of this session also get session.status
// through the pump; a repeat of the same state is idempotent on the client (it assigns, it doesn't
// accumulate), and the cost of one small frame per turn edge is worth an honest sidebar.
// publishSessionState announces derived turn state.
//
// The delivery is split deliberately, and the split is the fix for a real ordering defect.
//
// A connection has TWO independent outbound queues: the hub-level one (hubClient.ch, for
// cross-device broadcasts) and the per-session subscriber one (subscriber.ch, which carries a
// turn's content). Each is drained by its OWN goroutine writing to the same conn. Conn.Send
// serialises individual frames, but nothing orders the two queues against each other — so a status
// enqueued here could be written BEFORE deltas enqueued earlier on the session queue:
//
//	delta(here are the results:) | status:running | status:idle | COMPONENT | delta(done) | status:idle
//
// Measured at 11 of 60 runs in isolation. It matters because everything that finalises a turn does
// so on the terminal status: the spinner stops, the transcript is persisted, and the "agent
// finished" push is composed — all on an incomplete turn.
//
// So subscribers get this through the SESSION queue, behind that turn's content, and everyone else
// keeps getting it hub-level. broadcastExcept already exists for exactly this shape of problem.
func (m *managedSession) publishSessionState(status, detail string) {
	m.publishState(status, detail, false)
}

// publishStateRecorded is for an edge the DAEMON owns outright — an abandoned turn, a needs-you the
// nudge ladder gave up on, a reconciled idle. Those have no provider frame behind them, so unlike
// every other status there is no ringed twin: sent transiently they reached whoever happened to be
// attached at that instant and nobody else. A phone that was asleep, or any device attaching a
// second later, replayed a turn that simply stopped mid-sentence with no explanation of why.
//
// Ringed as well as delivered, so the reason survives to the next attach.
func (m *managedSession) publishStateRecorded(status, detail string) {
	m.publishState(status, detail, true)
}

func (m *managedSession) publishState(status, detail string, record bool) {
	if m.hub == nil || status == "" {
		return
	}
	payload := protocol.SessionStatus{SessionID: m.sess.ID(), Status: status, Detail: detail}
	if record {
		if raw, err := (agent.Event{Type: protocol.TypeSessionStatus, Payload: payload}).Encode(); err == nil {
			m.recordOnly(raw) // ring it even when nobody is subscribed — that is the case it exists for
		}
	}

	// Subscribers first, in the same queue as the content they are watching.
	skip := map[*transport.Conn]bool{}
	m.mu.Lock()
	for c := range m.subs {
		skip[c] = true
	}
	m.mu.Unlock()
	if len(skip) > 0 {
		if raw, err := (agent.Event{Type: protocol.TypeSessionStatus, Payload: payload}).Encode(); err == nil {
			// broadcastTransient, NOT broadcast: same subscriber queue (so the ordering guarantee
			// above holds identically) but no append to the replayable transcript ring.
			//
			// Derived turn state is ambient, not conversation. Sending it through broadcast put a
			// SECOND idle frame in every turn's replay, which a later attach would then replay as
			// history. This is the same argument surface.go already makes for broadcastFacts, and
			// the same one this file makes for turn.state — I made it twice elsewhere and still
			// reached for broadcast here.
			m.broadcastTransient(raw)
		}
	}
	// Everyone else — the Fleet, other devices not in this session — by the hub route.
	m.hub.broadcastExcept(protocol.TypeSessionStatus, payload, skip)
}

// clearNeedsYou retires this session's live needs-you items and tells every client, so the badge
// dies with the reason for it. See activity.Store.ClearNeedsYou.
func (m *managedSession) clearNeedsYou() {
	if m.hub != nil {
		m.hub.clearNeedsYou(m.sess.ID())
	}
}

// clearNeedsYou (hub-level) is the shared entry point: the turn edges call it, and so should any
// direct "the ask was answered" site (approval.respond).
func (h *Hub) clearNeedsYou(sessionID string) {
	h.mu.Lock()
	a := h.activity
	h.mu.Unlock()
	for _, e := range a.ClearNeedsYou(sessionID) { // nil-safe; returns only what it actually flipped
		h.broadcast(protocol.TypeActivityEvent, toProtoActivity(e))
	}
}

// emitTurn broadcasts the CURRENT (open) turn state; emitTurn2 broadcasts an explicit terminal state.
func (m *managedSession) emitTurn(reason string) {
	m.mu.Lock()
	if m.turnPhase == "" {
		m.mu.Unlock()
		return
	}
	// A stalled turn keeps its explanation across heartbeats, which carry no reason of their own.
	if reason == "" && m.turnPhase == protocol.StatusStalled {
		reason = m.turnStallReason
	}
	ts := m.turnSnapshotLocked(m.turnPhase, reason)
	m.mu.Unlock()
	m.sendTurn(ts)
}

func (m *managedSession) emitTurn2(state, reason string) {
	m.mu.Lock()
	ts := m.turnSnapshotLocked(state, reason)
	m.mu.Unlock()
	m.sendTurn(ts)
}

// handleUnreachable runs the recovery ladder for a probe that failed. It reports whether the turn is
// still alive (false = we gave up and said so).
//
// A failed probe is NOT evidence that the agent is gone, and treating it that way is what produced
// "agent unreachable: … context deadline exceeded" on a session whose server was answering fine —
// the probe simply took longer than its timeout, and four of those in twenty seconds killed the
// turn. So this does three things the old code didn't:
//
//  1. Tells a TIMEOUT apart from a refusal. A deadline means slow; only the transport actively
//     saying "nothing is listening" is evidence of absence.
//  2. Judges on ELAPSED TIME, not attempt count, so the verdict doesn't depend on the tick rate and
//     a laptop waking from sleep is not a dead agent.
//  3. Tries to REPAIR the connection (agent.Reviver) before reporting anything, because most of
//     these are a transport that can simply be rebuilt.
func (m *managedSession) handleUnreachable(probeErr error, fails, failLimit int) bool {
	now := time.Now()
	m.mu.Lock()
	if m.turnProbeSince.IsZero() {
		m.turnProbeSince = now
	}
	since := m.turnProbeSince
	m.turnProbeFails = fails + 1
	attempts := m.turnProbeFails
	revives := m.turnRevives
	m.mu.Unlock()
	down := now.Sub(since)

	// A timeout gets a longer rope than a refusal: a slow answer is still an answer trying to happen.
	window, slow, reviveCap := m.unreachWindow, m.slowWindow, m.reviveLimit
	if window <= 0 { // zero-valued managedSession (constructed directly in a test)
		window, slow, reviveCap = turnUnreachableWindow, turnSlowWindow, turnReviveLimit
	}
	if isTimeout(probeErr) {
		window = slow
	}

	// Try to repair in place, on a schedule of its own so a flapping connection isn't hammered.
	if reviver, ok := m.sess.(agent.Reviver); ok && attempts >= 2 && revives < reviveCap {
		m.mu.Lock()
		m.turnRevives = revives + 1
		m.turnPhase = protocol.StatusRecovering
		m.mu.Unlock()
		m.emitTurn("reconnecting to the agent")
		rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
		rerr := reviver.Revive(rctx)
		rcancel()
		if rerr == nil {
			// Repaired. Clear the outage and let the NEXT tick re-probe: Revive promises the session
			// is usable, not that the turn survived, and the probe is what actually knows.
			m.mu.Lock()
			m.turnProbeFails, m.turnProbeSince = 0, time.Time{}
			m.turnPhase = protocol.StatusRunning
			m.turnLastEvent = time.Now()
			m.mu.Unlock()
			m.emitTurn("")
			log.Printf("turn: session %s reconnected to its agent after %s down (attempt %d)", m.sess.ID(), down.Round(time.Second), revives+1)
			return true
		}
		log.Printf("turn: session %s revive attempt %d failed: %v", m.sess.ID(), revives+1, rerr)
	}

	if down < window {
		return true // still inside the grace window — keep trying, say nothing
	}
	// Out of rope. Report it honestly, and say what we tried so the reason is actionable rather
	// than a bare stack of transport noise.
	reason := fmt.Sprintf("agent unreachable for %s", down.Round(time.Second))
	if revives > 0 {
		reason += fmt.Sprintf(" (reconnected %d×, still failing)", revives)
	}
	reason += ": " + probeErr.Error()
	m.closeTurn(protocol.StatusAbandoned, reason)
	return false
}

// isTimeout reports whether an error is "too slow" rather than "not there". A deadline says the
// agent may well be alive and working; a refused connection says it is not.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

// resumeStalledTurn is called just before a NEW user prompt is delivered. It reports whether the
// open turn was stalled (so the caller may unstick it) and resets the stall bookkeeping — the
// progress clock, the spent nudges and the phase — because a user turn is, by definition, forward
// motion and the next stall verdict deserves a clean window.
func (m *managedSession) resumeStalledTurn() bool {
	m.mu.Lock()
	stalled := m.turnPhase == protocol.StatusStalled
	if m.turnPhase != "" {
		m.turnPhase = protocol.StatusRunning
	}
	m.turnToolAt = time.Now()
	m.turnNudges = 0
	m.turnStallReason = ""
	m.mu.Unlock()
	if stalled {
		m.emitTurn("")
	}
	return stalled
}

// escalateStalled handles a turn the provider calls busy but which has demonstrably stopped moving.
// It reports whether the turn is still OPEN afterwards (false = it closed as needs_you).
//
// The ladder deliberately never ends in an error and never kills the turn itself:
//
//	stalled → nudge → (still stuck) nudge → … → needs_you
//
// A nudge is a message asking the agent to continue, delivered over agent.Nudger — the channel that
// is contractually forbidden to abort the turn. If the agent picks it up, real events resume, the
// progress clock advances and the turn goes back to running with nobody bothered. If it doesn't, we
// stop guessing and hand it to a human. What we do NOT do is abort on our own initiative: the agent
// might be one tool-return away from finishing, and "the daemon killed my work on a hunch" is a
// worse failure than "the daemon asked for help".
func (m *managedSession) escalateStalled(stuckFor time.Duration) bool {
	nudger, canNudge := m.sess.(agent.Nudger)

	m.mu.Lock()
	// Mark the individual children that have gone quiet. The parent's clock is bumped by ANY child,
	// so without a per-child clock a fan-out with one chatty worker and nine dead ones looked
	// perfectly healthy — this is what turns that into "child 2 of 3 stalled".
	var stalledKids []string
	cutoff := time.Now().Add(-stuckFor)
	for _, k := range m.turnKids {
		if k.State != "running" && k.State != protocol.StatusStalled {
			continue
		}
		if k.LastEventAt > 0 && k.LastEventAt <= cutoff.Unix() {
			k.State = protocol.StatusStalled
			label := k.Title
			if label == "" {
				label = k.ID
			}
			stalledKids = append(stalledKids, label)
		}
	}
	var stuckTools []string
	for _, t := range m.turnTools {
		label := t.Name
		if t.Title != "" {
			label = t.Name + " · " + t.Title
		}
		stuckTools = append(stuckTools, label)
	}
	sort.Strings(stuckTools) // map order is random; a reason string that reshuffles every heartbeat is noise
	sort.Strings(stalledKids)
	spent := m.turnNudges
	limit := m.nudgeLimit
	if limit <= 0 {
		limit = turnNudgeLimit
	}
	m.turnPhase = protocol.StatusStalled
	m.mu.Unlock()

	reason := stallReason(stuckFor, stuckTools, stalledKids)

	// Out of nudges, or a provider that cannot take one (the generic CLI adapter refuses any prompt
	// while its turn runs). Either way there is nothing further to try automatically.
	if spent >= limit || !canNudge {
		why := reason
		if !canNudge {
			why = reason + " — this agent can't be nudged mid-turn"
		} else {
			why = reason + fmt.Sprintf(" — %d nudge(s) didn't move it", spent)
		}
		m.closeTurn(protocol.StatusNeedsYou, why)
		return false
	}

	m.mu.Lock()
	m.turnNudges = spent + 1
	m.turnStallReason = reason
	// Give the nudge a full no-progress window to land before we judge it. Without this the very
	// next tick would see the same stale clock and spend the whole budget in fifteen seconds.
	m.turnToolAt = time.Now()
	m.mu.Unlock()
	m.emitTurn(reason)
	m.publishSessionState(protocol.StatusRunning, "stalled — nudging")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := nudger.Nudge(ctx, stallNudge(stuckTools, stalledKids))
	cancel()
	if err != nil {
		log.Printf("turn: session %s stalled and the nudge failed: %v", m.sess.ID(), err)
		m.closeTurn(protocol.StatusNeedsYou, reason+" — couldn't reach the agent to nudge it")
		return false
	}
	log.Printf("turn: session %s stalled (%s), nudged %d/%d", m.sess.ID(), reason, spent+1, limit)
	return true
}

// stallReason is the human sentence shown wherever a stalled/needs-you turn surfaces.
func stallReason(stuckFor time.Duration, tools, kids []string) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "no progress for %s", stuckFor.Round(time.Minute))
	if len(tools) > 0 {
		b.WriteString(" — waiting on " + strings.Join(tools, ", "))
	}
	if len(kids) > 0 {
		b.WriteString(" — stalled sub-agent(s): " + strings.Join(kids, ", "))
	}
	return b.String()
}

// stallNudge is the message sent INTO the agent. It names what we think it is stuck on, because a
// bare "continue" to an agent mid-tool reads as a new instruction and can restart work it already
// did — telling it what we observed lets it either resume or explain that it is genuinely fine.
func stallNudge(tools, kids []string) string {
	b := &strings.Builder{}
	b.WriteString("You appear to have stopped making progress")
	switch {
	case len(tools) > 0:
		b.WriteString(" — the tool call(s) " + strings.Join(tools, ", ") + " never returned")
	case len(kids) > 0:
		b.WriteString(" — sub-agent(s) " + strings.Join(kids, ", ") + " went quiet")
	}
	b.WriteString(". If that tool or sub-agent is not going to return, abandon it and continue with " +
		"the rest of your plan (retry it differently, or work around it). If you are in fact still " +
		"working, ignore this message and carry on. If you are blocked on something only a human can " +
		"decide, say so explicitly and stop.")
	return b.String()
}

// toolSealNote explains, in the tool card itself, why it has no result — otherwise the card silently
// flips to "error" with an empty body and reads like the tool failed rather than never finishing.
func toolSealNote(state, reason string) string {
	switch state {
	case protocol.StatusIdle, protocol.StatusDone:
		return "the turn ended before this tool reported a result"
	case protocol.StatusNeedsYou:
		return "the turn stalled here and needs you: " + reason
	default:
		if reason != "" {
			return "the turn ended (" + state + "): " + reason
		}
		return "the turn ended (" + state + ") before this tool reported a result"
	}
}

func (m *managedSession) turnSnapshotLocked(state, reason string) protocol.TurnState {
	kids := make([]protocol.TurnChild, 0, len(m.turnKids))
	for _, k := range m.turnKids {
		kids = append(kids, *k)
	}
	tools := make([]protocol.TurnTool, 0, len(m.turnTools))
	for _, t := range m.turnTools {
		tools = append(tools, *t)
	}
	return protocol.TurnState{
		SessionID: m.sess.ID(), TurnID: m.turnID, State: state,
		StartedAt: m.turnStarted.Unix(), LastEventAt: m.turnLastEvent.Unix(),
		Detail: m.turnDetail, Reason: reason, Children: kids, Tools: tools,
		Nudges: m.turnNudges,
	}
}

func (m *managedSession) sendTurn(ts protocol.TurnState) {
	raw, err := (agent.Event{Type: protocol.TypeTurnState, Payload: ts}).Encode()
	if err != nil {
		return
	}
	m.broadcastTransient(raw)
}

// broadcastTransient fans an event to current subscribers WITHOUT recording it in the replayable
// transcript — turn.state is a live snapshot; replaying stale ones would lie about the present.
func (m *managedSession) broadcastTransient(raw []byte) {
	m.mu.Lock()
	subs := make([]*subscriber, 0, len(m.subs))
	for _, s := range m.subs {
		subs = append(subs, s)
	}
	m.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- raw:
		default:
			m.drop(s)
		}
	}
}

// turnLoops is the per-turn heartbeat + reconciler: while the turn is open it (a) emits turn.state at
// least every turnHeartbeatEvery (so the client can stay patient FOREVER on a slow-but-alive turn),
// and (b) when the provider has been silent past turnQuietAfter, asks the provider directly (Probe):
// busy → keep waiting; idle → we lost the completion event, recover the output + close; unreachable
// N× → abandon with the reason. This replaces every client-side timeout heuristic.
func (m *managedSession) turnLoops(stop chan struct{}) {
	// Snapshot the timings ONCE, into locals.
	//
	// These are package variables so tests can shrink them, and the loop used to read them on every
	// iteration — which raced the test harness restoring them in t.Cleanup while a previous test's
	// loop was still running. Reading them once, before the loop, means the loop's behaviour is fixed
	// at start and a later write cannot race a read that never happens again.
	hbEvery, quietAfter, reconcileTick, failLimit := m.hbEvery, m.quietAfter, m.reconcileTick, m.probeFailLimit
	noProgress := m.noProgressFor
	if reconcileTick <= 0 { // zero-valued managedSession (constructed directly in a test)
		hbEvery, quietAfter, reconcileTick, failLimit = turnHeartbeatEvery, turnQuietAfter, turnReconcileTick, turnProbeFailLimit
	}
	if noProgress <= 0 {
		noProgress = turnNoProgressFor
	}
	tick := time.NewTicker(reconcileTick)
	defer tick.Stop()
	lastHB := time.Now()
	// Probe pacing lives HERE rather than by bumping turnLastEvent, so that clock keeps meaning
	// "the provider last sent us something" — the only thing that makes it usable as evidence.
	var lastProbe time.Time
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		m.mu.Lock()
		open := m.turnPhase != ""
		lastEv := m.turnLastEvent
		fails := m.turnProbeFails
		m.mu.Unlock()
		if !open {
			return
		}
		if time.Since(lastHB) >= hbEvery {
			m.emitTurn("")
			lastHB = time.Now()
		}
		// Probe only when the provider has gone quiet AND we haven't just asked. The second half is
		// what the busy branch used to achieve by bumping turnLastEvent — done here so that clock
		// stays honest about what the AGENT did.
		if time.Since(lastEv) <= quietAfter || time.Since(lastProbe) < quietAfter {
			continue
		}
		lastProbe = time.Now()
		prober, ok := m.sess.(agent.Prober)
		if !ok {
			continue // subprocess providers: stream-end is authoritative; heartbeats keep the client patient
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		busy, err := prober.Probe(ctx)
		cancel()
		switch {
		case err != nil:
			if !m.handleUnreachable(err, fails, failLimit) {
				return // proven unrecoverable and reported
			}
		case busy:
			m.mu.Lock()
			m.turnProbeFails = 0
			// Deliberately does NOT touch turnLastEvent. That clock means "the provider last SENT us
			// something", and a probe answering is us asking, not the agent speaking. Conflating them
			// made turnLastEvent permanently fresh on any busy session, so it could never contribute to a
			// stall verdict. Probe pacing is handled by lastProbe below instead.
			// The provider answered, so whatever outage was being timed is over. An "unreachable for
			// N minutes" verdict has to mean N minutes of CONTINUOUS failure; letting the clock
			// survive a successful probe turns a series of unrelated hiccups into a death sentence.
			m.turnProbeSince = time.Time{}
			m.turnRevives = 0
			stuckFor := time.Since(m.turnToolAt)
			m.mu.Unlock()
			// "Busy" is necessary but nowhere near sufficient. A provider reports busy for a turn
			// wedged inside a single tool call exactly as it does for one that is thinking hard —
			// opencode's probe reads an incomplete assistant message, which is precisely what a hung
			// glob looks like. So a turn that is busy AND has had no tool start, no tool finish and no
			// sub-agent movement for minutes is the hang, and it used to heartbeat here forever.
			// Both clocks must be stale. turnToolAt tracks tool/child BOUNDARIES, so a single
			// long-running tool streaming output for ten minutes leaves it frozen while the agent is
			// plainly alive — requiring turnLastEvent to be quiet too means "nothing has happened at
			// all", which is what stalled was always supposed to mean.
			m.mu.Lock()
			quietFor := time.Since(m.turnLastEvent)
			// An agent waiting on a HUMAN is not stalled — it is doing exactly the right thing.
			//
			// This invariant is written on the field itself ("never nudge while > 0") and the older
			// heartbeat supervisor honours it; this ladder did not, and the result was the worst
			// loop in the system. A turn parked on an approval goes quiet by definition, so it trips
			// the no-progress rule, gets declared stalled, and is sent a message telling it that "if
			// you are blocked on something only a human can decide, say so explicitly and stop" —
			// which is precisely what it had already done. The nudges then queue up behind the
			// approval, and answering one only reveals the next.
			blocked := m.pendingApprovals > 0 || m.turnPhase == protocol.StatusAwaitingApproval
			m.mu.Unlock()
			if blocked {
				continue
			}
			if stuckFor > noProgress && quietFor > quietAfter {
				if !m.escalateStalled(stuckFor) {
					return // escalated all the way to needs_you; the turn is closed
				}
			}
		default: // provider says the turn is DONE — we missed the completion event
			if r, ok := m.sess.(agent.Recoverer); ok {
				rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
				r.Recover(rctx) // re-emits the final output through the normal event stream
				rcancel()
				// Recover hands its output to the PROVIDER's event channel, which the pump drains on
				// its own goroutine. Closing the turn straight away therefore raced the very content
				// that was just recovered, and the reconciled idle could land in front of it — the
				// same defect the subscriber-queue fix addressed, arriving by a different route
				// (producer ordering, not queue ordering).
				//
				// Deliberately a bounded WAIT rather than a barrier. turnLoops must never block on a
				// pump that can itself block (it appends to SQLite), so this observes a watermark and
				// gives up on a timer instead of waiting to be signalled. Worst case it closes a
				// little late, which costs nothing; a deadlock here would freeze the session.
				// Finish this turn ON THE PUMP, after the frames Recover just enqueued.
				//
				// Recover hands its output to the provider's channel, which the pump drains on its
				// own goroutine, so closing from here raced the very content that was recovered —
				// the same disorder the subscriber-queue fix addressed, arriving as producer
				// ordering rather than queue ordering.
				//
				// The flush matters as much as the order. flushUI hangs off the pump's handling of a
				// PROVIDER-sent idle, and a reconciled turn is by definition one where that never
				// arrived; the segmenter is line-oriented, so recovered output with no trailing
				// newline sat in it undelivered and an unterminated fence left a placeholder
				// spinning. The recovery could lose the tail it had just recovered.
				//
				// If the queue is full the task is refused rather than waited on, and we fall
				// through to closing directly: a turn that ends slightly out of order is a blemish,
				// a turn engine blocked on the pump is a frozen session.
				posted := m.onPump(func() {
					m.flushUI(m.sess.ID())
					m.closeTurn(protocol.StatusIdle, "reconciled: completion event was lost")
				})
				if posted {
					return
				}
			}
			m.closeTurn(protocol.StatusIdle, "reconciled: completion event was lost")
			return
		}
	}
}
