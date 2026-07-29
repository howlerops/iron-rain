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

type resubProvider struct{ sess *resubSession }

func (p *resubProvider) Name() string                                     { return "fake" }
func (p *resubProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *resubProvider) Create(context.Context, string, string) (agent.Session, error) {
	go p.sess.run()
	return p.sess, nil
}

type resubSession struct {
	events chan agent.Event
	done   chan struct{}
}

func (s *resubSession) ID() string                                     { return "rs" }
func (s *resubSession) Provider() string                              { return "fake" }
func (s *resubSession) Events() <-chan agent.Event                    { return s.events }
func (s *resubSession) Prompt(context.Context, string) error          { return nil }
func (s *resubSession) Respond(context.Context, string, string) error { return nil }
func (s *resubSession) Stop(context.Context) error                    { return nil }
func (s *resubSession) Close() error                                  { return nil }
func (s *resubSession) run() {
	s.events <- agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: "rs", Role: "assistant", Text: "yo"}}
	<-s.done
	close(s.events)
}

// TestResubscribeReplaysTranscript is the regression for "switching back to a session shows a blank
// pane / data never reloads": re-subscribing on a connection that's ALREADY subscribed must RE-SEND
// the transcript (it used to early-return with no replay).
func TestResubscribeReplaysTranscript(t *testing.T) {
	sess := &resubSession{events: make(chan agent.Event, 4), done: make(chan struct{})}
	defer close(sess.done)
	h := hub.New()
	h.Register(&resubProvider{sess: sess})
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	defer conn.Close()
	r := newReader(conn)

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	r.waitOK(t, "c1")

	countYo := func(label string) {
		r.waitFor(t, label, func(e protocol.Envelope) bool {
			if e.Type != protocol.TypeSessionMessage {
				return false
			}
			var m protocol.SessionMessage
			return json.Unmarshal(e.Payload, &m) == nil && m.Text == "yo"
		})
	}
	countYo("live message") // the creator is subscribed → sees the live "yo"

	// Re-subscribe on the SAME connection (a session switch back). Must replay "yo" AGAIN.
	send(t, conn, "s2", protocol.TypeSessionSubscribe, protocol.SessionRef{SessionID: "rs"})
	deadline := time.After(3 * time.Second)
	got := false
	for !got {
		select {
		case env, ok := <-r.ch:
			if !ok {
				t.Fatal("connection closed before the re-replay")
			}
			if env.Type == protocol.TypeSessionMessage {
				var m protocol.SessionMessage
				if json.Unmarshal(env.Payload, &m) == nil && m.Text == "yo" {
					got = true
				}
			}
		case <-deadline:
			t.Fatal("re-subscribe did NOT replay the transcript (blank-on-switch-back bug)")
		}
	}
}
