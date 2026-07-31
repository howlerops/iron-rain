package hub

import (
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestCloseTurnSealsChildrenOnEveryPath is the "dozens of stuck sub-agents" bug. The provider-level
// seal (opencode marking kids done on ITS session.idle) only covers the clean path — a turn ended by
// the reconciler, abandonment, or stream loss never got that event, and every sub-agent card spun
// forever. The invariant belongs at the one choke point all close paths share: closeTurn.
func TestCloseTurnSealsChildrenOnEveryPath(t *testing.T) {
	cases := []struct {
		closeState string
		wantSealed string
	}{
		{protocol.StatusIdle, "done"},
		{"abandoned", "error"}, // a dead turn's children must not be dressed as cleanly finished
		{protocol.StatusError, "error"},
	}
	for _, c := range cases {
		h := New()
		fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
		m := newManagedSession(h, fake, sessionMeta{})
		frames := make(chan []byte, 64)
		m.mu.Lock()
		m.subs[nil] = &subscriber{conn: nil, ch: frames, done: make(chan struct{})}
		m.mu.Unlock()

		m.openTurn("")
		// Dozens of children in flight, none of which ever reported done.
		for _, id := range []string{"kid-1", "kid-2", "kid-3"} {
			m.turnOnChild(protocol.SubAgent{ParentID: fake.ID(), ID: id, Status: "started"})
		}
		// One already finished — it must NOT be re-sealed (its state is real, not stale).
		m.turnOnChild(protocol.SubAgent{ParentID: fake.ID(), ID: "kid-done", Status: "done"})

		m.closeTurn(c.closeState, "test")

		// Collect the seal broadcasts.
		sealed := map[string]string{}
		deadline := time.After(2 * time.Second)
	drain:
		for {
			select {
			case raw := <-frames:
				env, err := protocol.Decode(raw)
				if err != nil || env.Type != protocol.TypeSessionSubAgent {
					continue
				}
				var sa protocol.SubAgent
				if env.Unmarshal(&sa) == nil {
					sealed[sa.ID] = sa.Status
				}
				if len(sealed) >= 3 {
					break drain
				}
			case <-deadline:
				break drain
			}
		}
		for _, id := range []string{"kid-1", "kid-2", "kid-3"} {
			if sealed[id] != c.wantSealed {
				t.Errorf("close %q: child %s sealed as %q, want %q", c.closeState, id, sealed[id], c.wantSealed)
			}
		}
		if _, reSealed := sealed["kid-done"]; reSealed {
			t.Errorf("close %q: an already-done child must not be re-sealed", c.closeState)
		}
		// The terminal snapshot itself must not carry running children either.
		m.mu.Lock()
		for id, k := range m.turnKids {
			if k.State != c.wantSealed && id != "kid-done" {
				t.Errorf("close %q: turnKids[%s] left %q in the terminal snapshot", c.closeState, id, k.State)
			}
		}
		m.mu.Unlock()
	}
}
