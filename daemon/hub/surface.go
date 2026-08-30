package hub

import (
	"context"

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
func (h *Hub) broadcastFacts(m *managedSession) {
	ev := agent.Event{Type: protocol.TypeSessionFacts, Payload: h.sessionFacts(m)}
	if raw, err := ev.Encode(); err == nil {
		m.broadcast(raw)
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
