package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// errRefused is a probe failure that means ABSENCE — nothing is listening — as opposed to a timeout,
// which only means the answer was slow. The Turn Engine treats them very differently: a refusal
// spends the (short) unreachable window, a timeout spends the far longer slow window, because a
// long session's own history read can outrun a probe deadline while the agent works perfectly.
var errRefused = errors.New("connect: connection refused")

// turnFakeSess is a scriptable provider session for Turn Engine tests: Probe/Recover behavior is
// injected per test.
type turnFakeSess struct {
	ch        chan agent.Event
	probe     func(context.Context) (bool, error)
	recovered atomic.Int32
}

func (f *turnFakeSess) ID() string                                    { return "t1" }
func (f *turnFakeSess) Provider() string                              { return "fake" }
func (f *turnFakeSess) Events() <-chan agent.Event                    { return f.ch }
func (f *turnFakeSess) Prompt(context.Context, string) error          { return nil }
func (f *turnFakeSess) Respond(context.Context, string, string) error { return nil }
func (f *turnFakeSess) Stop(context.Context) error                    { return nil }
func (f *turnFakeSess) Close() error                                  { return nil }
func (f *turnFakeSess) Probe(ctx context.Context) (bool, error)       { return f.probe(ctx) }
func (f *turnFakeSess) Recover(context.Context)                       { f.recovered.Add(1) }

// turnHarness builds a managedSession with an injected subscriber whose channel captures every
// subscriberConnID is a non-nil connection identity for the harness's session subscriber, so it is
// distinguishable from the hub-level observer registered at the nil key. Never dialled — it is only
// ever a map key here.
var subscriberConnID = &transport.Conn{}

// emitted frame, plus tiny Turn Engine timings (restored on cleanup).
func turnHarness(t *testing.T, probe func(context.Context) (bool, error)) (*managedSession, *turnFakeSess, chan []byte) {
	t.Helper()
	fake := &turnFakeSess{ch: make(chan agent.Event, 8), probe: probe}
	m := newManagedSession(New(), fake, sessionMeta{})
	// Tiny timings on THIS session. Mutating the package defaults instead raced the previous test's
	// still-running turn loop, which reads them when it starts.
	m.hbEvery, m.quietAfter, m.reconcileTick, m.probeFailLimit = 30*time.Millisecond, 50*time.Millisecond, 10*time.Millisecond, 3
	frames := make(chan []byte, 256)
	m.mu.Lock()
	// A DISTINCT connection identity, not nil.
	//
	// wireVerdictHub registers its observer as h.clients[nil] to model "connected to the daemon but
	// not subscribed to this session". Keying the subscriber at nil too made those one client, and
	// the tests only passed because per-session state used to be broadcast hub-wide to everyone
	// regardless. Now that a subscriber is served through the SESSION queue (so a turn's terminal
	// status cannot overtake its content) and skipped hub-side, the collision matters: the harness
	// has to model the two observers it describes.
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()
	return m, fake, frames
}

// nextTurnState waits for the next turn.state frame matching pred.
func nextTurnState(t *testing.T, frames chan []byte, what string, pred func(protocol.TurnState) bool) protocol.TurnState {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-frames:
			env, err := protocol.Decode(raw)
			if err != nil || env.Type != protocol.TypeTurnState {
				continue
			}
			var ts protocol.TurnState
			if json.Unmarshal(env.Payload, &ts) != nil {
				continue
			}
			if pred(ts) {
				return ts
			}
		case <-deadline:
			t.Fatalf("timeout waiting for turn.state: %s", what)
		}
	}
}

// TestTurnLifecycle: open → running frame; awaiting → frame; idle status → terminal idle frame and
// the turn is closed.
func TestTurnLifecycle(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.openTurn("running bash")
	ts := nextTurnState(t, frames, "running", func(ts protocol.TurnState) bool { return ts.State == protocol.StatusRunning })
	if ts.TurnID == "" || ts.Detail != "running bash" {
		t.Fatalf("bad open frame: %+v", ts)
	}
	m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusAwaitingApproval})
	nextTurnState(t, frames, "awaiting", func(ts protocol.TurnState) bool { return ts.State == protocol.StatusAwaitingApproval })
	m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusIdle})
	nextTurnState(t, frames, "idle", func(ts protocol.TurnState) bool { return ts.State == protocol.StatusIdle })
	m.mu.Lock()
	open := m.turnPhase != ""
	m.mu.Unlock()
	if open {
		t.Fatal("turn should be closed after idle")
	}
}

// TestTurnChildrenTracked: sub-agent events become turn children and appear in emitted frames.
func TestTurnChildrenTracked(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.openTurn("")
	m.turnOnChild(protocol.SubAgent{ParentID: "t1", ID: "kid1", Title: "explore", Status: "started"})
	ts := nextTurnState(t, frames, "child running", func(ts protocol.TurnState) bool {
		return len(ts.Children) == 1 && ts.Children[0].State == "running"
	})
	if ts.Children[0].Title != "explore" {
		t.Fatalf("child title lost: %+v", ts.Children)
	}
	m.turnOnChild(protocol.SubAgent{ParentID: "t1", ID: "kid1", Status: "done"})
	nextTurnState(t, frames, "child done", func(ts protocol.TurnState) bool {
		return len(ts.Children) == 1 && ts.Children[0].State == "done"
	})
	m.closeTurn(protocol.StatusIdle, "")
}

// TestTurnReconcilerPatientWhileBusy is the "never time out a working agent" guarantee: the provider
// is silent well past every threshold but Probe says busy — the turn must stay open, with heartbeats
// still flowing, and never abandon.
func TestTurnReconcilerPatientWhileBusy(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	m.openTurn("")
	// Wait through many quiet windows (quiet=50ms; wait 600ms ≈ 12 windows).
	time.Sleep(600 * time.Millisecond)
	m.mu.Lock()
	open := m.turnPhase != ""
	m.mu.Unlock()
	if !open {
		t.Fatal("turn was closed while the provider said busy — a false timeout")
	}
	// Heartbeats kept flowing during the quiet stretch.
	nextTurnState(t, frames, "heartbeat", func(ts protocol.TurnState) bool { return ts.State == protocol.StatusRunning })
	m.closeTurn(protocol.StatusIdle, "")
}

// TestTurnReconcilerRecoversLostIdle: the provider is quiet and Probe says NOT busy (the completion
// event was lost). The reconciler must call Recover (re-fetch the output) and close the turn idle.
func TestTurnReconcilerRecoversLostIdle(t *testing.T) {
	m, fake, frames := turnHarness(t, func(context.Context) (bool, error) { return false, nil })
	m.openTurn("")
	ts := nextTurnState(t, frames, "reconciled idle", func(ts protocol.TurnState) bool { return ts.State == protocol.StatusIdle })
	if ts.Reason == "" {
		t.Fatalf("reconciled close should carry a reason, got %+v", ts)
	}
	if fake.recovered.Load() == 0 {
		t.Fatal("Recover was not called — the lost final output would stay lost")
	}
}

// TestTurnReconcilerAbandonsUnreachable: an agent that stays unreachable for the whole outage window
// abandons the turn with a reason — the only path to a "no response" UI, and it is the daemon's
// verdict. The window is what's under test now, not a count of attempts: judging by attempts made
// the verdict depend on the tick rate, and at production timings four failures condemned an agent
// in twenty seconds.
func TestTurnReconcilerAbandonsUnreachable(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return false, errRefused })
	m.unreachWindow, m.slowWindow = 60*time.Millisecond, 60*time.Millisecond
	m.openTurn("")
	ts := nextTurnState(t, frames, "abandoned", func(ts protocol.TurnState) bool { return ts.State == "abandoned" })
	if ts.Reason == "" {
		t.Fatalf("abandoned frame must carry the reason, got %+v", ts)
	}
	m.mu.Lock()
	open := m.turnPhase != ""
	m.mu.Unlock()
	if open {
		t.Fatal("turn should be closed after abandonment")
	}
}
