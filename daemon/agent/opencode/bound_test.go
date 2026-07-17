package opencode

import (
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
)

// TestMapsBoundedOnIdle verifies the per-session bookkeeping maps (msgRoles / emittedUser)
// don't grow for the session's lifetime: once a turn ends (session.idle) the in-flight
// state for that turn is dropped, so the maps stay bounded across many turns.
func TestMapsBoundedOnIdle(t *testing.T) {
	s := &session{id: "sid", events: make(chan agent.Event, 64), done: make(chan struct{})}

	// Simulate several turns; each records a user message role + a forwarded user turn.
	for _, id := range []string{"m1", "m2", "m3"} {
		s.handle([]byte(`{"type":"message.updated","properties":{"info":{"id":"` + id + `","role":"user"}}}`))
		s.handle([]byte(`{"type":"message.part.updated","properties":{"part":{"type":"text","text":"hi","messageID":"` + id + `","sessionID":"sid"}}}`))
	}
	if len(s.msgRoles) == 0 || len(s.emittedUser) == 0 {
		t.Fatalf("expected in-flight bookkeeping populated, got msgRoles=%d emittedUser=%d", len(s.msgRoles), len(s.emittedUser))
	}

	// session.idle for this session ends the turn and must drop the bookkeeping.
	s.handle([]byte(`{"type":"session.idle","properties":{"sessionID":"sid"}}`))
	if len(s.msgRoles) != 0 || len(s.emittedUser) != 0 {
		t.Fatalf("maps not bounded on idle: msgRoles=%d emittedUser=%d", len(s.msgRoles), len(s.emittedUser))
	}

	// idle for a DIFFERENT session must not touch our state.
	s.handle([]byte(`{"type":"message.updated","properties":{"info":{"id":"m4","role":"user"}}}`))
	s.handle([]byte(`{"type":"session.idle","properties":{"sessionID":"other"}}`))
	if len(s.msgRoles) != 1 {
		t.Fatalf("idle for another session should not clear our maps, got msgRoles=%d", len(s.msgRoles))
	}
}
