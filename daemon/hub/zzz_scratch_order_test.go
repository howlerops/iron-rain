package hub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

type ordP struct{ sess *ordS }

func (p *ordP) Name() string                                     { return "fake" }
func (p *ordP) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *ordP) Create(context.Context, string, string) (agent.Session, error) {
	go p.sess.run()
	return p.sess, nil
}

type ordS struct{ events chan agent.Event }

func (s *ordS) ID() string                                    { return "ord2" }
func (s *ordS) Provider() string                              { return "fake" }
func (s *ordS) Events() <-chan agent.Event                    { return s.events }
func (s *ordS) Prompt(context.Context, string) error          { return nil }
func (s *ordS) Respond(context.Context, string, string) error { return nil }
func (s *ordS) Stop(context.Context) error                    { return nil }
func (s *ordS) Close() error                                  { return nil }
func (s *ordS) run() {
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "ord2", Text: "here are the results:\n```iron:ui\n{\"component\":\"tab"}}
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "ord2", Text: "le\",\"id\":\"r1\",\"props\":{\"columns\":[{\"label\":\"Test\"}],\"rows\":[[\"ok\"]]}}\n```\ndone\n"}}
	s.events <- agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: "ord2", Status: protocol.StatusIdle}}
	close(s.events)
}

func TestScratchOrdering(t *testing.T) {
	const runs = 200
	bad := 0
	for i := 0; i < runs; i++ {
		if !scratchOne(t) {
			bad++
		}
	}
	fmt.Printf("REORDERED %d/%d\n", bad, runs)
}

func scratchOne(t *testing.T) bool {
	h := hub.New()
	h.Register(&ordP{sess: &ordS{events: make(chan agent.Event, 8)}})
	kp, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, kp)
	r := newReader(conn)
	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	r.waitOK(t, "c1")
	sawDone, sawComp := false, false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-r.ch:
			if !ok {
				return sawDone && sawComp
			}
			switch e.Type {
			case protocol.TypeUIComponent:
				sawComp = true
			case protocol.TypeOutputDelta:
				var d protocol.OutputDelta
				_ = json.Unmarshal(e.Payload, &d)
				if containsStr(d.Text, "done") {
					sawDone = true
				}
			case protocol.TypeSessionStatus:
				var st protocol.SessionStatus
				_ = json.Unmarshal(e.Payload, &st)
				if st.Status == protocol.StatusIdle || st.Status == protocol.StatusDone {
					return sawDone && sawComp
				}
			}
		case <-deadline:
			return sawDone && sawComp
		}
	}
}
