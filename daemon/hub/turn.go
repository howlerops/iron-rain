package hub

import (
	"context"
	"log"
	"time"

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
	m.emitTurn("")
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
		}
	case protocol.StatusIdle, protocol.StatusDone:
		m.closeTurn(protocol.StatusIdle, "")
	case protocol.StatusError:
		m.closeTurn(protocol.StatusError, ss.Detail)
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
// Idempotent: only the first close wins.
func (m *managedSession) closeTurn(state, reason string) {
	m.mu.Lock()
	if m.turnPhase == "" {
		m.mu.Unlock()
		return
	}
	m.turnPhase = ""
	stop := m.turnStopLoop
	m.turnStopLoop = nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	m.emitTurn2(state, reason)
	if state == protocol.StatusError || state == "abandoned" {
		log.Printf("turn: session %s closed %s: %s", m.sess.ID(), state, reason)
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
	tick := time.NewTicker(turnReconcileTick)
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
		if time.Since(lastHB) >= turnHeartbeatEvery {
			m.emitTurn("")
			lastHB = time.Now()
		}
		if time.Since(lastEv) <= turnQuietAfter {
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
			exceeded := m.turnProbeFails >= turnProbeFailLimit
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
