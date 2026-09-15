package agentsim

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// The simulator's own tests.
//
// This package has no behaviour a user ever sees, which is exactly why it needs them: every
// assertion in the chaos suite is only as good as the claim that a scenario emitted what its name
// says. A Fanout(10) that quietly emitted three children, or an As override that did not override,
// would leave those tests green while testing nothing — the tautology trap, one layer down and
// invisible from above because the suite reads the hub's output, not the sim's input.
//
// So these check the sim against its own promises: the events, in order, with the ids they claim.

// collect drains a session's events until the stream closes or the budget expires.
func collect(t *testing.T, s *Session, within time.Duration) []agent.Event {
	t.Helper()
	var out []agent.Event
	ch := s.Events()
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}

func statusesOf(evs []agent.Event) []string {
	var out []string
	for _, ev := range evs {
		if ss, ok := ev.Payload.(protocol.SessionStatus); ok {
			out = append(out, ss.Status)
		}
	}
	return out
}

// A scenario's steps reach the stream in order, as the right event types.
func TestTheScriptIsEmittedInOrder(t *testing.T) {
	s := New("sim", HappyTurn())
	evs := collect(t, s, time.Second)

	var types []string
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	want := []string{
		protocol.TypeSessionStatus,  // running
		protocol.TypeSessionMessage, // looking at the file
		protocol.TypeSessionTool,    // read
		protocol.TypeSessionMessage, // here is the answer
		protocol.TypeSessionStatus,  // idle
	}
	if len(types) != len(want) {
		t.Fatalf("the happy scenario emitted %d events (%v), want %d (%v) — every chaos test that "+
			"uses it as its control is measuring a different script than the one it reads",
			len(types), types, len(want), want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d is %q, want %q", i, types[i], want[i])
		}
	}
	if got := statusesOf(evs); len(got) != 2 || got[0] != protocol.StatusRunning || got[1] != protocol.StatusIdle {
		t.Fatalf("the happy scenario's statuses are %v, want [running idle]", got)
	}
}

// Probe's default: busy while the script runs, not busy once it has finished.
//
// This is what makes "the provider told the truth" scenarios mean anything. A sim that always
// reported not-busy would let the reconciler close every turn instantly, and the lost-idle test
// would pass without the recovery path ever being the reason.
func TestProbeReportsBusyUntilTheScriptEnds(t *testing.T) {
	s := New("sim", Scenario{Steps: []Step{{Kind: Running}, {Kind: Delay, For: 150 * time.Millisecond}}})
	_ = s.Events() // start the script

	busy, err := s.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !busy {
		t.Fatal("the provider reported NOT busy while its script was still running: every scenario " +
			"that depends on the provider telling the truth is testing the opposite of what it says")
	}

	deadline := time.After(2 * time.Second)
	for !s.Done() {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatal("the script never finished")
		}
	}
	if busy, _ := s.Probe(context.Background()); busy {
		t.Fatal("the provider still reports busy after its script ended — a turn driven by this " +
			"scenario can never be closed on provider truth, so the reconciler tests would hang " +
			"rather than assert")
	}
}

// A scenario's own Busy function overrides the default, including reporting a refusal.
func TestAScenarioCanOverrideTheProbe(t *testing.T) {
	refused := errors.New("connect: connection refused")
	s := New("sim", Scenario{Busy: func(bool) (bool, error) { return false, refused }})
	if _, err := s.Probe(context.Background()); !errors.Is(err, refused) {
		t.Fatalf("Probe returned %v, want the scenario's own refusal — the unreachable scenario is "+
			"built entirely on this, and without it the probe silently succeeds", err)
	}
}

// The As override actually changes the session id an event is emitted under.
//
// Load-bearing for the fanout routing test: a child speaks through the PARENT's stream carrying its
// OWN id, and if As were ignored the event would arrive as the parent's own idle — which would end
// the parent turn and make that test assert the exact opposite of its name.
func TestAsEmitsUnderAnotherSessionID(t *testing.T) {
	s := New("parent", Scenario{Steps: []Step{
		{Kind: Emit, Text: "mine"},
		{Kind: Emit, Text: "the child's", As: "kid_0"},
		{Kind: Idle, As: "kid_0"},
		{Kind: Idle},
	}})
	evs := collect(t, s, time.Second)

	var ids []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case protocol.SessionMessage:
			ids = append(ids, p.SessionID)
		case protocol.SessionStatus:
			ids = append(ids, p.SessionID)
		}
	}
	want := []string{"parent", "kid_0", "kid_0", "parent"}
	if len(ids) != len(want) {
		t.Fatalf("got %d events, want %d", len(ids), len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("event %d carries session id %q, want %q — a child's event is arriving as the "+
				"parent's own, which is the failure the routing test claims to rule out", i, ids[i], want[i])
		}
	}
}

// DropStream closes the stream WITHOUT an idle, and nothing scripted after it is delivered.
func TestDropStreamEndsTheStreamWithNoCompletion(t *testing.T) {
	s := New("sim", Scenario{Steps: []Step{
		{Kind: Running},
		{Kind: DropStream},
		{Kind: Emit, Text: "must never arrive"},
		{Kind: Idle},
	}})
	evs := collect(t, s, time.Second)

	for _, ev := range evs {
		if ss, ok := ev.Payload.(protocol.SessionStatus); ok && ss.Status == protocol.StatusIdle {
			t.Fatal("a dropped stream delivered an idle: the scenario is a clean completion wearing " +
				"the name of a transport failure, and the tests built on it prove nothing")
		}
		if msg, ok := ev.Payload.(protocol.SessionMessage); ok && msg.Text == "must never arrive" {
			t.Fatal("an event scripted AFTER the stream dropped was delivered")
		}
	}
	// And the channel really is closed, not merely quiet.
	select {
	case _, ok := <-s.Events():
		if ok {
			t.Fatal("the stream is still open after DropStream")
		}
	case <-time.After(time.Second):
		t.Fatal("the stream never closed after DropStream — the hub would wait on it forever")
	}
}

// Recover replays the scenario's tail, and is counted.
func TestRecoverReplaysTheLostTail(t *testing.T) {
	s := New("sim", LostIdle())
	ch := s.Events()
	// Drain what the script emits before it goes quiet.
	var got []agent.Event
	for len(got) < 2 {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-time.After(time.Second):
			t.Fatalf("the lost-idle scenario emitted only %d events before going quiet", len(got))
		}
	}

	s.Recover(context.Background())
	if s.Recovers() != 1 {
		t.Fatalf("Recovers() = %d, want 1", s.Recovers())
	}

	var sawTail, sawIdle bool
	deadline := time.After(time.Second)
	for !(sawTail && sawIdle) {
		select {
		case ev := <-ch:
			if msg, ok := ev.Payload.(protocol.SessionMessage); ok && msg.Text == "the tail the hub never saw" {
				sawTail = true
			}
			if ss, ok := ev.Payload.(protocol.SessionStatus); ok && ss.Status == protocol.StatusIdle {
				sawIdle = true
			}
		case <-deadline:
			t.Fatalf("Recover did not replay the tail (tail=%v idle=%v). The lost-idle test asserts "+
				"that the hub called Recover AND that the turn closed; if Recover emits nothing, "+
				"the close came from somewhere else and that test is measuring the wrong thing",
				sawTail, sawIdle)
		}
	}
}

// Close and Stop are idempotent: the stream is closed once, whoever gets there first.
//
// A send racing a close panics in Go, and a panic in a test binary takes every other test with it.
// The hub closes sessions from more than one goroutine, so this is not hypothetical.
func TestCloseAndStopAreIdempotent(t *testing.T) {
	s := New("sim", Burst(500))
	_ = s.Events()
	time.Sleep(5 * time.Millisecond) // let the script get going and start sending

	done := make(chan struct{}, 4)
	for i := 0; i < 2; i++ {
		go func() { _ = s.Close(); done <- struct{}{} }()
		go func() { _ = s.Stop(context.Background()); done <- struct{}{} }()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a concurrent Close/Stop never returned")
		}
	}
}

// The builders emit what their names promise.
func TestTheBuildersProduceTheShapeTheyName(t *testing.T) {
	t.Run("fanout spawns and ends every child", func(t *testing.T) {
		started, ended := childCounts(t, New("p", Fanout(10)))
		if started != 10 || ended != 10 {
			t.Fatalf("Fanout(10) started %d and ended %d children, want 10 and 10 — the parent test "+
				"asserts it saw at least 2 concurrent children and that none were left running",
				started, ended)
		}
	})

	t.Run("child-idle-lost drops exactly one end", func(t *testing.T) {
		started, ended := childCounts(t, New("p", FanoutChildIdleLost(4)))
		if started != 4 || ended != 3 {
			t.Fatalf("FanoutChildIdleLost(4) started %d and ended %d, want 4 and 3. With 4 and 4 "+
				"there is no lost event and the test is the plain fanout under another name.",
				started, ended)
		}
	})

	t.Run("burst emits every delta", func(t *testing.T) {
		s := New("p", Burst(200))
		var deltas int
		for _, ev := range collect(t, s, 3*time.Second) {
			if _, ok := ev.Payload.(protocol.SessionMessage); ok {
				deltas++
			}
		}
		if deltas != 200 {
			t.Fatalf("Burst(200) emitted %d messages — the seq-monotonicity test requires at least "+
				"100 sequenced frames to reach the client, and it is counting these", deltas)
		}
	})

	t.Run("child-speaks-idle keeps the parent busy", func(t *testing.T) {
		sc := ChildSpeaksIdle()
		if sc.Busy == nil {
			t.Fatal("ChildSpeaksIdle has no Busy override, so the PARENT stops reporting busy once " +
				"the script parks — the routing test would then pass because the turn ended for an " +
				"entirely different reason")
		}
		busy, err := sc.Busy(true)
		if !busy || err != nil {
			t.Fatalf("its probe reports busy=%v err=%v after the script parks, want busy with no error", busy, err)
		}
		// The only idle in the script must belong to the CHILD.
		for _, st := range sc.Steps {
			if st.Kind == Idle && st.As == "" {
				t.Fatal("the scenario contains a PARENT idle; the parent finishing on its own is " +
					"not what the routing test means to observe")
			}
		}
	})
}

func childCounts(t *testing.T, s *Session) (started, ended int) {
	t.Helper()
	for _, ev := range collect(t, s, 3*time.Second) {
		sa, ok := ev.Payload.(protocol.SubAgent)
		if !ok {
			continue
		}
		if sa.Status == "started" {
			started++
		} else {
			ended++
		}
	}
	return started, ended
}
