package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// The client actually watching the session got an approval with NO narrow scopes on it.
//
// The daemon computes, per request, the scopes an "always allow" could be narrowed to — the exact
// command pattern, the directory, this project — so the user is never forced to choose between
// answering once and granting a tool everywhere. `surfaceApproval` fills them in through a pointer,
// and the pump assigned the event payload BEFORE calling it. The payload is a value, so it captured
// a copy taken one line too early: subscribers got an empty scope list, while the hub-wide broadcast
// that deliberately SKIPS those subscribers carried the full one.
//
// The consequence is not a missing menu. The client falls back to a single broad button when scopes
// are empty, so the only offer on the screen the user is looking at is "Always allow bash
// everywhere" — one tap writing a rule that covers every project and every session on the machine,
// forever, while the narrow alternatives sat in a frame sent to every OTHER device.
func TestTheWatchingClientGetsTheNarrowScopes(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}
	m := newManagedSession(h, &subSess{ch: make(chan agent.Event, 4)}, sessionMeta{projectID: "proj_1"})

	sub := &subscriber{conn: subscriberConnID, ch: make(chan []byte, 64), done: make(chan struct{})}
	m.mu.Lock()
	m.subs[subscriberConnID] = sub
	m.mu.Unlock()

	go m.run()
	for i := 0; i < 500 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.pumpAlive.Load() {
		t.Fatal("the pump never started")
	}

	m.sess.(*subSess).ch <- agent.Event{
		Type: protocol.TypeApprovalRequest,
		Payload: protocol.ApprovalRequest{
			ApprovalID: "ap1", SessionID: m.sess.ID(), Tool: "bash",
			Detail: "npm test", Patterns: []string{"npm *"},
		},
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case raw := <-sub.ch:
			var env struct {
				Type    string                   `json:"type"`
				Payload protocol.ApprovalRequest `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || env.Type != protocol.TypeApprovalRequest {
				continue
			}
			if len(env.Payload.SuggestedScopes) == 0 {
				t.Fatal("the approval reached the client watching this session with no suggested " +
					"scopes — the only thing it can offer is \"Always allow bash everywhere\", which " +
					"grants the tool in every project and every session on the machine")
			}
			// The narrow ones specifically: a list containing only the broad fallback is the same
			// failure wearing a longer array.
			var sawPattern bool
			for _, s := range env.Payload.SuggestedScopes {
				if s.Kind == "pattern" && s.Value == "npm *" {
					sawPattern = true
				}
			}
			if !sawPattern {
				t.Errorf("scopes = %+v, missing the harness's own pattern", env.Payload.SuggestedScopes)
			}
			return
		case <-deadline:
			t.Fatal("no approval request reached the subscriber")
		}
	}
}
