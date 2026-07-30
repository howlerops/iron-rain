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

// genuiProvider creates a session that streams an assistant delta containing an ```iron:ui``` fence,
// then goes idle — exercising the daemon's segmenter → ui.component wire path end to end.
type genuiProvider struct{ sess *genuiSession }

func (p *genuiProvider) Name() string                                     { return "fake" }
func (p *genuiProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *genuiProvider) Create(context.Context, string, string) (agent.Session, error) {
	go p.sess.run()
	return p.sess, nil
}

type genuiSession struct{ events chan agent.Event }

func (s *genuiSession) ID() string                                    { return "genui_sess" }
func (s *genuiSession) Provider() string                              { return "fake" }
func (s *genuiSession) Events() <-chan agent.Event                    { return s.events }
func (s *genuiSession) Prompt(context.Context, string) error          { return nil }
func (s *genuiSession) Respond(context.Context, string, string) error { return nil }
func (s *genuiSession) Stop(context.Context) error                    { return nil }
func (s *genuiSession) Close() error                                  { return nil }
func (s *genuiSession) run() {
	// A fence split across TWO deltas, with plain text on both sides — proves streaming reassembly.
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "genui_sess", Text: "here are the results:\n```iron:ui\n{\"component\":\"tab"}}
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "genui_sess", Text: "le\",\"id\":\"r1\",\"props\":{\"columns\":[{\"label\":\"Test\"}],\"rows\":[[\"ok\"]]}}\n```\ndone\n"}}
	s.events <- agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: "genui_sess", Status: protocol.StatusIdle}}
	close(s.events)
}

// TestGenUIFenceProducesComponent verifies that an ```iron:ui``` fence in the assistant text stream
// is pulled out and re-emitted as a normalized ui.component event, while the surrounding prose still
// streams as output.delta with the fence removed.
func TestGenUIFenceProducesComponent(t *testing.T) {
	h := hub.New()
	h.Register(&genuiProvider{sess: &genuiSession{events: make(chan agent.Event, 8)}})
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	r.waitOK(t, "c1")

	// A ui.component event should arrive carrying the decoded table.
	var comp protocol.UIComponent
	r.waitFor(t, "ui.component", func(e protocol.Envelope) bool {
		if e.Type != protocol.TypeUIComponent {
			return false
		}
		_ = json.Unmarshal(e.Payload, &comp)
		return true
	})
	if comp.Component != "table" || comp.ID != "r1" {
		t.Fatalf("expected table r1, got component=%q id=%q", comp.Component, comp.ID)
	}
	if comp.Status != "ready" {
		t.Fatalf("expected status ready, got %q", comp.Status)
	}
	if comp.SessionID != "genui_sess" {
		t.Fatalf("component session id not stamped: %q", comp.SessionID)
	}

	// The surrounding prose still streams, but the fence JSON must be gone from any output.delta.
	// Walk events until the idle status; assert no fence leaked and that real prose came through.
	sawText := false
	r.waitFor(t, "session idle", func(e protocol.Envelope) bool {
		if e.Type == protocol.TypeOutputDelta {
			var d protocol.OutputDelta
			_ = json.Unmarshal(e.Payload, &d)
			if containsStr(d.Text, "iron:ui") || containsStr(d.Text, "\"component\"") {
				t.Fatalf("fence JSON leaked into output.delta: %q", d.Text)
			}
			if containsStr(d.Text, "results") || containsStr(d.Text, "done") {
				sawText = true
			}
		}
		return e.Type == protocol.TypeSessionStatus
	})
	if !sawText {
		t.Fatal("surrounding prose never streamed as output.delta")
	}
}

func containsStr(hay, needle string) bool {
	return len(hay) >= len(needle) && (indexOf(hay, needle) >= 0)
}
func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
