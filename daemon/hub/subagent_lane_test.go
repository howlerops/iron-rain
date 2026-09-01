package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// subSess streams a sub-agent's output the way claude-code and opencode do: an announcement for the
// lane, then deltas addressed to the CHILD id rather than the session's own.
type subSess struct{ ch chan agent.Event }

func (s *subSess) ID() string                                    { return "sub_parent" }
func (s *subSess) Provider() string                              { return "fake" }
func (s *subSess) Events() <-chan agent.Event                    { return s.ch }
func (s *subSess) Prompt(context.Context, string) error          { return nil }
func (s *subSess) Respond(context.Context, string, string) error { return nil }
func (s *subSess) Stop(context.Context) error                    { return nil }
func (s *subSess) Close() error                                  { return nil }

// A sub-agent's report has to survive the turn that produced it.
//
// The lane announcement was durable and its CONTENTS were not: sub-agent text is only ever streamed
// as deltas and no provider finalizes it into a message, so re-opening a session showed the lane,
// showed "done", and expanded to "no output" — the work looked like it had produced nothing.
//
// Asserts both halves: the child's text is NOT delivered live a second time (watchers already had
// the deltas), and it IS in the replayable history addressed to the child, so an attach after the
// fact can still read it.
func TestSubAgentOutputSurvivesTheTurn(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sess := &subSess{ch: make(chan agent.Event, 16)}
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, sess, sessionMeta{})
	frames := make(chan []byte, 128)
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()

	go m.run()
	for i := 0; i < 200 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.pumpAlive.Load() {
		t.Fatal("the pump never started")
	}

	const child = "toolu_child1"
	sess.ch <- agent.Event{Type: protocol.TypeSessionSubAgent,
		Payload: protocol.SubAgent{ParentID: sess.ID(), ID: child, Title: "Review the store", Status: "started"}}
	sess.ch <- agent.Event{Type: protocol.TypeOutputDelta,
		Payload: protocol.OutputDelta{SessionID: child, Text: "Found a data race on the cache map."}}
	sess.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: sess.ID(), Status: protocol.StatusIdle}}

	// The delta itself must reach the watcher addressed to the CHILD — that is what fills the lane
	// live. The finalized copy must not, or the report renders twice.
	sawDelta := false
	deadline := time.After(5 * time.Second)
	for !sawDelta {
		select {
		case raw := <-frames:
			if strings.Contains(string(raw), protocol.TypeOutputDelta) && strings.Contains(string(raw), child) {
				sawDelta = true
			}
		case <-deadline:
			t.Fatal("the sub-agent's delta never reached the subscriber under the child id")
		}
	}
	drain := time.After(400 * time.Millisecond)
	for done := false; !done; {
		select {
		case raw := <-frames:
			if strings.Contains(string(raw), protocol.TypeSessionMessage) && strings.Contains(string(raw), child) {
				t.Fatalf("the finalized sub-agent message was delivered live, duplicating the deltas: %s", raw)
			}
		case <-drain:
			done = true
		}
	}

	// And it has to be in the history, addressed to the child, or a later attach shows an empty lane.
	found := false
	for _, raw := range m.fullHistory() {
		var f struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		if f.Type == protocol.TypeSessionMessage && f.Payload.SessionID == child &&
			strings.Contains(f.Payload.Text, "data race") {
			found = true
		}
	}
	if !found {
		t.Fatal("the sub-agent's report is not in the replayable history — the lane will read as empty")
	}
}

// A sealed lane must keep its title.
//
// The turn-close seal is written with advanceDurable under the lane's stable id, so it REPLACES the
// lane's stored row rather than appending one. Built without the Title, it overwrote the only durable
// record of what the sub-agent was for — re-opening the session showed a correctly-sealed lane that
// no longer said what it had been asked to do.
func TestSealedSubAgentLaneKeepsItsTitle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sess := &subSess{ch: make(chan agent.Event, 16)}
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, sess, sessionMeta{})
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: make(chan []byte, 256), done: make(chan struct{})}
	m.mu.Unlock()

	go m.run()
	for i := 0; i < 500 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}

	const child, title = "toolu_kid", "Review the store"
	sess.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: sess.ID(), Status: protocol.StatusRunning}}
	sess.ch <- agent.Event{Type: protocol.TypeSessionSubAgent,
		Payload: protocol.SubAgent{ParentID: sess.ID(), ID: child, Title: title, Status: "started"}}
	// The turn ends without the lane ever reporting done — the case the seal exists for.
	sess.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: sess.ID(), Status: protocol.StatusIdle}}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var sealed *protocol.SubAgent
		for _, raw := range m.fullHistory() {
			var f struct {
				Type    string            `json:"type"`
				Payload protocol.SubAgent `json:"payload"`
			}
			if json.Unmarshal(raw, &f) == nil && f.Type == protocol.TypeSessionSubAgent && f.Payload.ID == child {
				cp := f.Payload
				sealed = &cp
			}
		}
		if sealed != nil && protocol.IsSubAgentFinished(sealed.Status) {
			if sealed.Title != title {
				t.Fatalf("the sealed lane's title is %q, want %q — the seal overwrote the lane's only "+
					"durable row and the replayed card no longer says what it was for", sealed.Title, title)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the lane was never sealed")
}
