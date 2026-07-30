package hub_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

// statusScopeProvider's session emits a PARENT "running" status, then a CHILD (sub-agent) "idle"
// status — the shape of a fanout where a sub-agent finishes while the parent turn is still going.
type statusScopeProvider struct{ sess *statusScopeSession }

func (p *statusScopeProvider) Name() string                                     { return "fake" }
func (p *statusScopeProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *statusScopeProvider) Create(context.Context, string, string) (agent.Session, error) {
	go p.sess.run()
	return p.sess, nil
}

type statusScopeSession struct {
	events chan agent.Event
	done   chan struct{}
}

func (s *statusScopeSession) ID() string                                    { return "parent_x" }
func (s *statusScopeSession) Provider() string                              { return "fake" }
func (s *statusScopeSession) Events() <-chan agent.Event                    { return s.events }
func (s *statusScopeSession) Prompt(context.Context, string) error          { return nil }
func (s *statusScopeSession) Respond(context.Context, string, string) error { return nil }
func (s *statusScopeSession) Stop(context.Context) error                    { return nil }
func (s *statusScopeSession) Close() error                                  { return nil }
func (s *statusScopeSession) run() {
	s.events <- agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: "parent_x", Status: protocol.StatusRunning}}
	s.events <- agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: "child_x", Status: protocol.StatusIdle}}
	<-s.done // stay live so it lists
	close(s.events)
}

// TestSubAgentStatusDoesNotEndParentTurn is the regression for the hub-level scoping bug: a sub-agent's
// forwarded status (SessionID == a child id) must NOT drive the PARENT's turn state. Before the fix, a
// child "idle" flipped the parent's lastStatus to idle (and turnEnded=true), so the parent looked
// finished mid-turn — a fanout would show the parent done the moment any sub-agent idled.
func TestSubAgentStatusDoesNotEndParentTurn(t *testing.T) {
	sess := &statusScopeSession{events: make(chan agent.Event, 8), done: make(chan struct{})}
	defer close(sess.done)
	h := hub.New()
	h.Register(&statusScopeProvider{sess: sess})
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	defer conn.Close()
	r := newReader(conn)

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	r.waitOK(t, "c1")

	// Wait until the CHILD idle status has been delivered (so run() has processed it) before checking.
	r.waitFor(t, "child idle status", func(e protocol.Envelope) bool {
		if e.Type != protocol.TypeSessionStatus {
			return false
		}
		var ss protocol.SessionStatus
		if json.Unmarshal(e.Payload, &ss) != nil {
			return false
		}
		return ss.SessionID == "child_x" && ss.Status == protocol.StatusIdle
	})

	// The PARENT must still read as running — the child idle must not have ended its turn.
	send(t, conn, "l1", protocol.TypeSessionList, struct{}{})
	var list protocol.SessionList
	if err := json.Unmarshal(r.waitOK(t, "l1"), &list); err != nil {
		t.Fatal(err)
	}
	var parent *protocol.Session
	for i := range list.Sessions {
		if list.Sessions[i].ID == "parent_x" {
			parent = &list.Sessions[i]
		}
	}
	if parent == nil {
		t.Fatalf("parent session missing from list %+v", list.Sessions)
	}
	if parent.Status != protocol.StatusRunning {
		t.Fatalf("parent turn was ended by a sub-agent's idle: status=%q (want running)", parent.Status)
	}
}
