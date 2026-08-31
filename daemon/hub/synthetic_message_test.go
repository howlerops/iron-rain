package hub

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// streamSess is a delta-only provider — claude-code, pi and the CLI family all behave this way: they
// stream assistant text and never send a finalized assistant message of their own.
type streamSess struct{ ch chan agent.Event }

func (s *streamSess) ID() string                                    { return "str_sess" }
func (s *streamSess) Provider() string                              { return "fake" }
func (s *streamSess) Events() <-chan agent.Event                    { return s.ch }
func (s *streamSess) Prompt(context.Context, string) error          { return nil }
func (s *streamSess) Respond(context.Context, string, string) error { return nil }
func (s *streamSess) Stop(context.Context) error                    { return nil }
func (s *streamSess) Close() error                                  { return nil }

// A turn that ends with a generative-UI component must not restate its own prose.
//
// The daemon synthesizes an end-of-turn assistant message for delta-only providers so the durable
// transcript holds the reply. That frame was BROADCAST, which meant clients already holding the
// deltas received the same text a second time. It only showed up when the streamed row was no longer
// the last one — a ui.component seals it and appends its own row — so the reply rendered as prose,
// card, then the identical prose again.
//
// Asserts the split: live subscribers get the deltas and the component and nothing else; the ring
// still gets the finalized frame, because a later attach has no deltas to rebuild it from.
func TestSyntheticTurnMessageIsNotDeliveredLive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	sess := &streamSess{ch: make(chan agent.Event, 16)}
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

	const prose = "Here is the migration plan.\n"
	sess.ch <- agent.Event{Type: protocol.TypeOutputDelta,
		Payload: protocol.OutputDelta{SessionID: sess.ID(), Text: prose}}
	// A complete, valid iron:ui fence: the segmenter pulls it out of the text and re-emits it as a
	// component, which is what appends the row that seals the streamed one on the client.
	sess.ch <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{
		SessionID: sess.ID(),
		Text:      "```iron:ui\n{\"component\":\"callout\",\"id\":\"c1\",\"props\":{\"level\":\"info\",\"body\":\"done\"}}\n```\n",
	}}
	sess.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: sess.ID(), Status: protocol.StatusIdle}}

	var (
		sawComponent bool
		deadline     = time.After(5 * time.Second)
	)
	for !sawComponent {
		select {
		case raw := <-frames:
			switch {
			case strings.Contains(string(raw), protocol.TypeUIComponent):
				sawComponent = true
			case strings.Contains(string(raw), protocol.TypeSessionMessage):
				t.Fatalf("the synthesized end-of-turn message was delivered live, duplicating text the "+
					"subscriber already received as deltas: %s", raw)
			}
		case <-deadline:
			t.Fatal("never saw the generative-UI component")
		}
	}

	// Idle arrives after the component; drain briefly and hold the same assertion — the synthetic
	// frame is written on idle, so checking only up to the component would pass on the broken code.
	drain := time.After(300 * time.Millisecond)
	for done := false; !done; {
		select {
		case raw := <-frames:
			if strings.Contains(string(raw), protocol.TypeSessionMessage) {
				t.Fatalf("the synthesized end-of-turn message was delivered live: %s", raw)
			}
		case <-drain:
			done = true
		}
	}

	// It must still be in the replayable history: an attach that arrives after the turn has no
	// deltas to rebuild the reply from, so dropping the frame entirely would lose the answer.
	found := false
	for _, raw := range m.fullHistory() {
		if strings.Contains(string(raw), protocol.TypeSessionMessage) && strings.Contains(string(raw), "migration plan") {
			found = true
		}
	}
	if !found {
		t.Fatal("the finalized assistant message never reached the replayable history")
	}
}
