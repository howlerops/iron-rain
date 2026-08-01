package hub

import (
	"context"
	"log"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
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
	m.turnDetail = detail
	m.turnKids = map[string]*protocol.TurnChild{}
	m.turnProbeFails = 0
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
	}
	m.mu.Unlock()
}

// turnOnStatus folds the session's OWN status events into the turn state machine.
func (m *managedSession) turnOnStatus(ss protocol.SessionStatus) {
	switch ss.Status {
	case protocol.StatusRunning:
		m.mu.Lock()
		open := m.turnPhase != ""
		changed := m.turnPhase == protocol.StatusAwaitingApproval
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
	state := "running"
	switch sa.Status {
	case "done":
		state = "done"
	case "error":
		state = "error"
	}
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
		kid = &protocol.TurnChild{ID: sa.ID}
		m.turnKids[sa.ID] = kid
	}
	kid.State = state
	if sa.Title != "" {
		kid.Title = sa.Title
	}
	m.turnLastEvent = time.Now()
	m.mu.Unlock()
	m.emitTurn("")
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
	if m.hub != nil && m.hub.awake != nil {
		defer m.hub.awake.Release() // balanced with the Hold in openTurn
	}
	// Seal the children. The provider-level seal (opencode marks its kids done when ITS session.idle
	// arrives) only covers the clean path — a turn ended by the reconciler, by abandonment, or by
	// stream loss never got that event, and a fan-out's sub-agent cards then spun forever with no way
	// to recover short of restarting the app. The parent's turn being over means no child can still
	// be running, whatever state we last heard for it; this is the one choke point every close path
	// shares, so the invariant lives here.
	sealed := "done"
	if state == protocol.StatusError || state == "abandoned" {
		sealed = "error" // don't dress a dead turn's children as cleanly finished
	}
	var toSeal []protocol.SubAgent
	for _, k := range m.turnKids {
		if k.State != "done" && k.State != "error" {
			k.State = sealed
			toSeal = append(toSeal, protocol.SubAgent{ParentID: m.sess.ID(), ID: k.ID, Status: sealed})
		}
	}
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	// Broadcast the seals as ordinary sub-agent events so every subscriber's per-card state resolves —
	// the terminal turn snapshot alone doesn't reach the inline cards, which key off these.
	for _, sa := range toSeal {
		if raw, err := (agent.Event{Type: protocol.TypeSessionSubAgent, Payload: sa}).Encode(); err == nil {
			m.broadcast(raw)
		}
	}
	if len(toSeal) > 0 {
		log.Printf("turn: session %s sealed %d sub-agent(s) as %s on %s close", m.sess.ID(), len(toSeal), sealed, state)
	}
	m.emitTurn2(state, reason)
	m.publishVerdict(state, reason, providerDriven)
	if state == protocol.StatusError || state == "abandoned" {
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
	failed := state == protocol.StatusError || state == "abandoned"
	status := protocol.StatusIdle
	if failed {
		status = protocol.StatusError
	}
	m.mu.Lock()
	// A turn open when the user pressed Stop ends as "abandoned" through the stream-death path. That
	// is the human's own doing: record the state, but don't page them about an error they caused.
	if m.userStopped {
		failed, status = false, protocol.StatusIdle
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
	stopped := m.userStopped
	m.mu.Unlock()

	m.publishSessionState(status, reason)
	if stopped {
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
func (m *managedSession) publishSessionState(status, detail string) {
	if m.hub == nil || status == "" {
		return
	}
	m.hub.broadcast(protocol.TypeSessionStatus, protocol.SessionStatus{
		SessionID: m.sess.ID(), Status: status, Detail: detail,
	})
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

func (m *managedSession) turnSnapshotLocked(state, reason string) protocol.TurnState {
	kids := make([]protocol.TurnChild, 0, len(m.turnKids))
	for _, k := range m.turnKids {
		kids = append(kids, *k)
	}
	return protocol.TurnState{
		SessionID: m.sess.ID(), TurnID: m.turnID, State: state,
		StartedAt: m.turnStarted.Unix(), LastEventAt: m.turnLastEvent.Unix(),
		Detail: m.turnDetail, Reason: reason, Children: kids,
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
	if reconcileTick <= 0 { // zero-valued managedSession (constructed directly in a test)
		hbEvery, quietAfter, reconcileTick, failLimit = turnHeartbeatEvery, turnQuietAfter, turnReconcileTick, turnProbeFailLimit
	}
	tick := time.NewTicker(reconcileTick)
	defer tick.Stop()
	lastHB := time.Now()
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
		if time.Since(lastEv) <= quietAfter {
			continue
		}
		prober, ok := m.sess.(agent.Prober)
		if !ok {
			continue // subprocess providers: stream-end is authoritative; heartbeats keep the client patient
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		busy, err := prober.Probe(ctx)
		cancel()
		switch {
		case err != nil:
			m.mu.Lock()
			m.turnProbeFails = fails + 1
			exceeded := m.turnProbeFails >= failLimit
			m.mu.Unlock()
			if exceeded {
				m.closeTurn("abandoned", "agent unreachable: "+err.Error())
				return
			}
		case busy:
			m.mu.Lock()
			m.turnProbeFails = 0
			m.turnLastEvent = time.Now() // provider vouched for itself — reset the quiet clock
			m.mu.Unlock()
		default: // provider says the turn is DONE — we missed the completion event
			if r, ok := m.sess.(agent.Recoverer); ok {
				rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
				r.Recover(rctx) // re-emits the final output through the normal event stream
				rcancel()
			}
			m.closeTurn(protocol.StatusIdle, "reconciled: completion event was lost")
			return
		}
	}
}
