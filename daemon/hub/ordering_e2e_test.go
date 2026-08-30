package hub_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

// orderingProvider streams a turn's content and then goes idle — the simplest possible shape of
// "a turn produced output and finished".
type orderingProvider struct{ sess *orderingSession }

func (p *orderingProvider) Name() string                                     { return "fake" }
func (p *orderingProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *orderingProvider) Create(context.Context, string, string) (agent.Session, error) {
	go p.sess.run()
	return p.sess, nil
}

type orderingSession struct{ events chan agent.Event }

func (s *orderingSession) ID() string                                    { return "ord_sess" }
func (s *orderingSession) Provider() string                              { return "fake" }
func (s *orderingSession) Events() <-chan agent.Event                    { return s.events }
func (s *orderingSession) Prompt(context.Context, string) error          { return nil }
func (s *orderingSession) Respond(context.Context, string, string) error { return nil }
func (s *orderingSession) Stop(context.Context) error                    { return nil }
func (s *orderingSession) Close() error                                  { return nil }
func (s *orderingSession) run() {
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "ord_sess", Text: "alpha "}}
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "ord_sess", Text: "omega"}}
	s.events <- agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: "ord_sess", Status: protocol.StatusIdle}}
	close(s.events)
}

// A turn's CONTENT must reach the client before that turn's TERMINAL STATUS.
//
// Anything that finalises on idle — the spinner stopping, the transcript being persisted, the
// "agent finished" push being composed — acts on whatever has arrived by then. If idle overtakes the
// content, all three act on an incomplete turn.
//
// Run many times because the defect is a race, measured at roughly 8% of runs.
func TestTerminalStatusNeverPrecedesItsTurnContent(t *testing.T) {
	const runs = 60
	bad := 0
	for i := 0; i < runs; i++ {
		if !contentPrecededIdle(t) {
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("%d/%d runs delivered the terminal status BEFORE the turn's own content", bad, runs)
	}
}

// contentPrecededIdle reports whether the last delta arrived before the first terminal status.
func contentPrecededIdle(t *testing.T) bool {
	t.Helper()
	h := hub.New()
	h.Register(&orderingProvider{sess: &orderingSession{events: make(chan agent.Event, 8)}})
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	r.waitOK(t, "c1")

	sawOmega := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-r.ch:
			if !ok {
				return sawOmega
			}
			switch e.Type {
			case protocol.TypeOutputDelta:
				var d protocol.OutputDelta
				_ = json.Unmarshal(e.Payload, &d)
				if containsStr(d.Text, "omega") {
					sawOmega = true
				}
			case protocol.TypeSessionStatus:
				var st protocol.SessionStatus
				_ = json.Unmarshal(e.Payload, &st)
				if st.Status == protocol.StatusIdle || st.Status == protocol.StatusDone {
					// The first terminal status decides it: by this point the content must be here.
					return sawOmega
				}
			}
		case <-deadline:
			return sawOmega
		}
	}
}
