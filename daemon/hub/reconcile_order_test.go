package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// recoverSess is a provider whose completion event goes missing: it reports NOT busy when probed,
// and re-emits its final output through the normal event stream when Recover is called — which is
// exactly what opencode's reconciler path does.
type recoverSess struct {
	ch        chan agent.Event
	recovered chan struct{}
}

func (s *recoverSess) ID() string                                    { return "rec_sess" }
func (s *recoverSess) Provider() string                              { return "fake" }
func (s *recoverSess) Events() <-chan agent.Event                    { return s.ch }
func (s *recoverSess) Prompt(context.Context, string) error          { return nil }
func (s *recoverSess) Respond(context.Context, string, string) error { return nil }
func (s *recoverSess) Stop(context.Context) error                    { return nil }
func (s *recoverSess) Close() error                                  { return nil }

// Probe: the turn is done — the daemon simply never saw the completion event.
func (s *recoverSess) Probe(context.Context) (bool, error) { return false, nil }

// Recover pushes the lost output back through the event stream, as the real ones do.
func (s *recoverSess) Recover(context.Context) {
	s.ch <- agent.Event{Type: protocol.TypeOutputDelta,
		// NO trailing newline, deliberately. The segmenter is line-oriented, so a partial line is
		// held until something flushes it — and on the reconcile path the provider idle that the
		// pump's flush hangs off never arrives. Without the flush added to that path this text is
		// lost entirely, which is the recovery losing the very tail it recovered.
		Payload: protocol.OutputDelta{SessionID: "rec_sess", Text: "RECOVERED-TAIL"}}
	close(s.recovered)
}

// The reconciler recovers a lost completion by asking the provider to re-emit its final output. That
// output goes to the PUMP's goroutine, while the reconciler closes the turn on its own — so closing
// immediately raced the content it had just asked for, and the "reconciled" idle could land in front
// of it.
//
// Distinct from the subscriber-queue defect: both queues are correct here, and the disorder is
// between two PRODUCERS.
func TestReconciledIdleFollowsRecoveredOutput(t *testing.T) {
	sess := &recoverSess{ch: make(chan agent.Event, 8), recovered: make(chan struct{})}
	m := newManagedSession(New(), sess, sessionMeta{})
	frames := make(chan []byte, 64)
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()

	go m.run()
	// Wait for the pump before driving the reconcile.
	//
	// Not papering over the race — removing one that cannot occur in production. A turn is only
	// open because the pump delivered the events that opened it, so the reconciler never runs
	// against a pump that has not started. Without this the test raced its own setup and exercised
	// the no-pump fallback, which is by definition the unordered path.
	for i := 0; i < 200 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.pumpAlive.Load() {
		t.Fatal("the pump never started")
	}
	m.openTurn("running bash")

	// Drive the reconcile directly rather than waiting out the heartbeat timers.
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sess.Recover(rctx)
		// Honour onPump's contract, exactly as the reconciler does: a refusal means nobody would
		// ever run the task, so the caller must do the work itself. Ignoring the result left the
		// turn permanently open whenever this goroutine beat the pump to starting.
		finish := func() {
			m.flushUI(m.sess.ID())
			m.closeTurn(protocol.StatusIdle, "reconciled: completion event was lost")
		}
		if !m.onPump(finish) {
			finish()
		}
	}()

	sawTail := false
	deadline := time.After(8 * time.Second)
	for {
		select {
		case raw := <-frames:
			var env struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil {
				continue
			}
			switch env.Type {
			case protocol.TypeOutputDelta:
				var d protocol.OutputDelta
				_ = json.Unmarshal(env.Payload, &d)
				if d.Text == "RECOVERED-TAIL" {
					sawTail = true
				}
			case protocol.TypeSessionStatus:
				var st protocol.SessionStatus
				_ = json.Unmarshal(env.Payload, &st)
				if st.Status == protocol.StatusIdle || st.Status == protocol.StatusDone {
					if !sawTail {
						t.Fatal("the reconciled idle arrived BEFORE the output it had just recovered")
					}
					return
				}
			}
		case <-deadline:
			t.Fatal("no terminal status after the reconcile")
		}
	}
}
