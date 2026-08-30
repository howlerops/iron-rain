package hub

import (
	"context"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// Telling a client what a session CAN do and what is currently TRUE about it.
//
// These two events are why the adapter layer stopped being a lowest-common-denominator. Previously
// every provider emitted the same nine event types, so the app could only ever render the
// intersection of four harnesses — which meant the richest provider was described in the vocabulary
// of the poorest, and the app disagreed with that provider's own TUI sitting next to it about what
// the session could even do.

// sendSessionSurface sends the capability manifest and the current facts to ONE connection.
//
// Sent synchronously on the connection rather than through the subscriber channel, and before
// subscribe registers that channel: it must arrive ahead of the transcript replay, or the client
// paints its first screenful with no mode indicator and no affordances and pops them in afterwards.
func (h *Hub) sendSessionSurface(conn *transport.Conn, m *managedSession) {
	caps := agent.CapabilitiesOf(m.sess)
	h.sendEvent(conn, protocol.TypeSessionCapabilities, caps)
	h.sendEvent(conn, protocol.TypeSessionFacts, h.sessionFacts(m))
}

// broadcastFacts republishes a session's facts to everyone watching it. Used when the daemon — not
// the provider — changes something the status bar shows, the mode being the one that matters: the
// user switches to yolo on their phone and the Mac must stop showing "Normal".
//
// Sent straight to each watcher's connection rather than through managedSession.broadcast, for two
// reasons that both bit:
//
//   - broadcast APPENDS to the replayable transcript ring. Facts are ambient state, not conversation,
//     so every mode change would have been persisted as history and replayed on every future attach,
//     growing the ring with frames that describe a moment that has passed.
//   - broadcast drops a subscriber whose outbound buffer is full. Switching mode right after
//     subscribing therefore raced the transcript replay still draining into that buffer, and the
//     loser was not the event — it was the whole subscription. The test for this failed consistently
//     and then passed when a log line was added, which is what a race looks like when you are lucky
//     enough to see it.
func (h *Hub) broadcastFacts(m *managedSession) {
	facts := h.sessionFacts(m)
	m.mu.Lock()
	conns := make([]*transport.Conn, 0, len(m.subs))
	for c := range m.subs {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	for _, c := range conns {
		h.sendEvent(c, protocol.TypeSessionFacts, facts)
	}
}

// sessionFacts merges what the provider reports with what only the daemon knows.
//
// The mode is deliberately the DAEMON's value, overriding anything the provider said. Modes are
// enforced here, against the approval layer, so the hub's value is the one that decides what
// actually happens — and a status bar showing a mode the enforcement does not agree with is worse
// than showing none, particularly for yolo.
func (h *Hub) sessionFacts(m *managedSession) protocol.SessionFacts {
	facts := agent.FactsOf(context.Background(), m.sess)
	facts.SessionID = m.sess.ID()
	facts.Mode = m.sessionMode()
	// The context meter. Used comes from the last turn's cache-read + new input — the size of the
	// conversation actually sent — and the window from the model the session is running.
	//
	// Only reported when BOTH are known. A meter with no denominator can show a number but not how
	// close to full you are, which is the one thing it exists to answer, and inventing a default
	// window would put a confident wrong percentage in the status bar.
	m.mu.Lock()
	used, model := m.contextTokens, m.model
	m.mu.Unlock()
	if used > 0 {
		facts.ContextUsed = used
		if limit := h.contextLimitFor(m.sess.Provider(), model); limit > 0 {
			facts.ContextMax = limit
		}
	}
	if facts.CWD == "" {
		m.mu.Lock()
		facts.CWD = m.meta.cwd
		m.mu.Unlock()
	}
	if facts.Branch == "" && facts.CWD != "" {
		facts.Branch = agent.GitBranch(facts.CWD)
	}
	return facts
}

// contextLimitFor resolves a model's context window, cached because it comes from a provider round
// trip and changes only when the provider's configuration does.
//
// Returns 0 when unknown, and every caller treats 0 as "do not render a meter" rather than
// substituting a guess: a percentage computed against the wrong window is worse than no percentage,
// because it will be believed.
func (h *Hub) contextLimitFor(provider, model string) int {
	if provider == "" || model == "" {
		return 0
	}
	key := provider + "/" + model
	h.mu.Lock()
	if h.contextLimits == nil {
		h.contextLimits = map[string]int{}
	}
	if v, ok := h.contextLimits[key]; ok {
		h.mu.Unlock()
		return v
	}
	prov := h.providers[provider]
	h.mu.Unlock()

	lister, ok := prov.(agent.ModelLister)
	if !ok {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := lister.Models(ctx)
	if err != nil {
		return 0
	}
	limit := 0
	h.mu.Lock()
	for _, mi := range models {
		if mi.ContextLimit > 0 {
			h.contextLimits[mi.Provider+"/"+mi.ID] = mi.ContextLimit
			h.contextLimits[provider+"/"+mi.ID] = mi.ContextLimit
		}
		if mi.ID == model {
			limit = mi.ContextLimit
		}
	}
	h.mu.Unlock()
	return limit
}
