package hub

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// A turn that ends in ERROR must still persist what it streamed, and must not carry it forward.
//
// finalizeTurnTranscript used to hang off the pump's idle/done branch alone, and it is also the only
// thing that clears asstAccum. So a turn ending any other way — RUN_ERROR, a dead stream, probe
// abandonment, a reconciled close — did two damaging things at once:
//
//  1. its reply never reached the durable transcript, so it vanished on restart or ring trim; and
//  2. the text stayed in the accumulator and was PREPENDED to the next turn's synthetic message, so
//     the earlier answer reappeared on screen underneath a later prompt, attributed to the wrong turn.
//
// That reached every delta-only provider (claude-code, pi, cli, and AG-UI now that it no longer
// finalizes a message of its own), not just the one it was found on.
func TestErroredTurnPersistsItsReplyAndDoesNotLeakIntoTheNext(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sess := &subSess{ch: make(chan agent.Event, 32)}
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, sess, sessionMeta{})
	frames := make(chan []byte, 512)
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()

	go m.run()
	for i := 0; i < 500 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.pumpAlive.Load() {
		t.Fatal("the pump never started")
	}

	sid := sess.ID()
	send := func(ev agent.Event) { sess.ch <- ev }
	status := func(st, detail string) {
		send(agent.Event{Type: protocol.TypeSessionStatus,
			Payload: protocol.SessionStatus{SessionID: sid, Status: st, Detail: detail}})
	}
	delta := func(text string) {
		send(agent.Event{Type: protocol.TypeOutputDelta,
			Payload: protocol.OutputDelta{SessionID: sid, Text: text}})
	}

	// Turn one: streams a reply, then FAILS.
	status(protocol.StatusRunning, "")
	delta("TURN-ONE-REPLY")
	status(protocol.StatusError, "the backend blew up")

	// Turn two: a fresh prompt, a fresh reply, a clean end.
	status(protocol.StatusRunning, "")
	delta("TURN-TWO-REPLY")
	status(protocol.StatusIdle, "")

	// Wait for the second turn to be finalized rather than sleeping a fixed amount.
	var msgs []string
	deadline := time.After(10 * time.Second)
	for {
		msgs = assistantMessages(t, m, sid)
		if len(msgs) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d assistant message(s) were ever persisted: %q", len(msgs), msgs)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	for _, got := range msgs {
		if strings.Contains(got, "TURN-ONE-REPLY") && strings.Contains(got, "TURN-TWO-REPLY") {
			t.Fatalf("the errored turn's reply leaked into the next turn's message: %q", got)
		}
	}
	if !hasExactly(msgs, "TURN-ONE-REPLY") {
		t.Errorf("the errored turn's reply never reached the durable transcript: %q", msgs)
	}
	if !hasExactly(msgs, "TURN-TWO-REPLY") {
		t.Errorf("the successful turn's reply is missing: %q", msgs)
	}
}

func hasExactly(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// assistantMessages returns the text of every finalized assistant message stored for sid.
func assistantMessages(t *testing.T, m *managedSession, sid string) []string {
	t.Helper()
	var out []string
	for _, raw := range m.fullHistory() {
		var f struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				Role      string `json:"role"`
				Text      string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		if f.Type == protocol.TypeSessionMessage && f.Payload.Role == "assistant" && f.Payload.SessionID == sid {
			out = append(out, f.Payload.Text)
		}
	}
	return out
}
