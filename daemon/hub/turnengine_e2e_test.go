package hub

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/agentsim"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/transport"
)

// Turn Engine stage 5: every scripted provider failure, driven through the REAL managedSession and
// judged on what a CLIENT would see.
//
// The distinction from the turn_*_test.go files next to this one is deliberate and is the reason
// this file exists at all. Those reach inside — they call openTurn directly, read m.turnPhase, and
// assert on internal fields. That is right for pinning a specific mechanism and wrong for the
// question stage 5 asks, which is whether the state a client RENDERS converges on the truth. So
// everything here asserts on turn.state frames off a subscriber's channel, the same bytes the app
// decodes, and nothing here reads a field the wire does not carry.
//
// On timing: these run the real reconciler with the real loop, compressed to milliseconds via the
// per-session knobs. There is no fake clock — the plan asked for one, and the honest report is that
// the turn loop reads wall time in a dozen places (turnLastEvent, turnToolAt, the unreachable and
// slow windows) and threading a clock through all of them is a refactor of turn.go, not a test.
// Compressed real time gets the same convergence assertions; what it does not get is immunity from a
// loaded machine, so every wait here is generous relative to the tick it is waiting on.

// simHarness wires a scripted session into a real managedSession with compressed Turn Engine
// timings, and returns the frame channel a client would be reading.
func simHarness(t *testing.T, sc agentsim.Scenario) (*managedSession, *agentsim.Session, chan []byte) {
	t.Helper()
	sim := agentsim.New("sim_"+sc.Name, sc)
	// A REAL store, not a bare hub. seq is stamped on the durable-append path, so a hub with no
	// database emits unsequenced frames — and the seq assertions below would then pass by finding
	// nothing to check. It also means every scenario here exercises the SQLite write the daemon
	// actually performs, rather than a memory-only shortcut no deployment runs.
	db, err := store.Open(filepath.Join(t.TempDir(), "sim.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := New()
	h.SetStore(db)
	m := newManagedSession(h, sim, sessionMeta{})
	m.hbEvery, m.quietAfter, m.reconcileTick, m.probeFailLimit = 20*time.Millisecond, 40*time.Millisecond, 10*time.Millisecond, 3
	m.noProgressFor, m.nudgeLimit = 150*time.Millisecond, 2
	m.unreachWindow, m.slowWindow = 80*time.Millisecond, 200*time.Millisecond

	frames := make(chan []byte, 4096)
	conn := &transport.Conn{}
	m.mu.Lock()
	m.subs[conn] = &subscriber{conn: conn, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()
	t.Cleanup(func() { _ = sim.Close() })
	return m, sim, frames
}

// awaitState waits for a turn.state frame in one of want, and fails naming what it saw instead.
// Reporting the states actually observed matters more than it looks: a convergence failure and a
// converged-to-the-wrong-answer failure need different fixes, and "timeout" alone cannot tell them
// apart.
func awaitState(t *testing.T, frames chan []byte, within time.Duration, want ...string) protocol.TurnState {
	t.Helper()
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
	}
	var seen []string
	deadline := time.After(within)
	for {
		select {
		case raw := <-frames:
			ts, ok := decodeTurn(raw)
			if !ok {
				continue
			}
			if len(seen) == 0 || seen[len(seen)-1] != ts.State {
				seen = append(seen, ts.State)
			}
			if wanted[ts.State] {
				return ts
			}
		case <-deadline:
			t.Fatalf("no turn.state in %v reached any of %v; the client saw %v", within, want, seen)
		}
	}
}

func decodeTurn(raw []byte) (protocol.TurnState, bool) {
	env, err := protocol.Decode(raw)
	if err != nil || env.Type != protocol.TypeTurnState {
		return protocol.TurnState{}, false
	}
	var ts protocol.TurnState
	if json.Unmarshal(env.Payload, &ts) != nil {
		return protocol.TurnState{}, false
	}
	return ts, true
}

// Scenario 1 — the happy turn. The control for the whole file: if a turn that completes normally
// does not reach idle, no failure assertion below means anything.
func TestSimHappyTurnClosesCleanly(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.HappyTurn())
	go m.run()
	ts := awaitState(t, frames, 3*time.Second, protocol.StatusIdle)
	if ts.Reason != "" {
		t.Fatalf("a turn that finished normally closed with reason %q — the client shows an "+
			"explanation for something that did not go wrong", ts.Reason)
	}
}

// Scenario 2 — the completion event is LOST while the stream stays open.
//
// The provider is no longer busy but never said so. This is the failure the reconciler exists for:
// no event will ever arrive to close this turn, and the only evidence available is a probe. The turn
// must close on that evidence, and it must close as a normal completion — not an error — because
// nothing went wrong with the AGENT. Losing this distinction is how a working turn came to page
// someone.
func TestSimLostIdleIsRecoveredNotErrored(t *testing.T) {
	m, sim, frames := simHarness(t, agentsim.LostIdle())
	go m.run()

	ts := awaitState(t, frames, 3*time.Second, protocol.StatusIdle)
	if ts.State != protocol.StatusIdle {
		t.Fatalf("a completed turn whose idle event was lost ended as %q", ts.State)
	}
	if sim.Recovers() == 0 {
		t.Fatal("the hub closed the turn without ever calling Recover, so the output the provider " +
			"still held was thrown away — the turn ends correctly and the answer is missing")
	}
	if sim.Probes() == 0 {
		t.Fatal("the turn closed without a single probe: whatever closed it was not provider truth")
	}
}

// Scenario 3 — the provider's event stream ends while it still reports busy.
//
// This is NOT the "transport blipped" case, and finding that out is worth recording. At the hub's
// abstraction a closed Events() channel IS the end of the session: every adapter absorbs its own
// reconnects below that line (opencode retries the SSE internally and only closes after its retries
// are exhausted), and the hub responds by detaching the session entirely. So there is no version of
// this where output still arrives — the only question is whether the client is left spinning.
//
// It must not be. The turn closes promptly, as abandoned, with a reason.
//
// One honest caveat on that reason: "the agent's event stream ended" is also what a user sees when
// opencode merely lost its connection to an agent that is still working server-side. The verdict is
// right (nothing further can reach this daemon) and the wording overstates what is known.
func TestSimStreamEndClosesTheTurnRatherThanSpinning(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.DroppedStream(true))
	go m.run()

	ts := awaitState(t, frames, 3*time.Second, protocol.StatusAbandoned, protocol.StatusIdle,
		protocol.StatusError, protocol.StatusNeedsYou)
	if ts.State != protocol.StatusAbandoned {
		t.Fatalf("the stream ended mid-turn and the turn closed as %q; abandoned is the state that "+
			"says the turn could not finish, which is what happened", ts.State)
	}
	if ts.Reason == "" {
		t.Fatal("abandoned with no reason: the client can only say the turn stopped")
	}
}

// Scenario 4a — no false timeout: a quiet turn the provider reports as BUSY must stay open, and the
// client must keep hearing heartbeats.
//
// This is the property the whole engine was built for, and the one the old client-side watchdog got
// wrong: it counted silence as death, so a long build or a slow inference call ended a turn that was
// working perfectly. Silence is not evidence — the probe is.
//
// noProgressFor is raised well past the window under test so the stall detector cannot rescue the
// assertion: if anything closes this turn inside the window, it is a false timeout, not a stall
// verdict.
func TestSimQuietButBusyTurnIsNotTimedOut(t *testing.T) {
	m, sim, frames := simHarness(t, agentsim.WedgedBusy())
	m.noProgressFor = 30 * time.Second
	go m.run()

	// quietAfter is 40ms and hbEvery 20ms, so this window is many multiples of both.
	window := time.After(500 * time.Millisecond)
	var heartbeats int
	for {
		select {
		case raw := <-frames:
			ts, ok := decodeTurn(raw)
			if !ok {
				continue
			}
			switch ts.State {
			case protocol.StatusRunning:
				heartbeats++
			case protocol.StatusIdle, protocol.StatusError, protocol.StatusAbandoned:
				t.Fatalf("a turn the provider reports as BUSY was closed as %q after going quiet. "+
					"That is a false timeout: the agent is still working and the client has just "+
					"been told it is not.", ts.State)
			}
		case <-window:
			if heartbeats < 3 {
				t.Fatalf("only %d heartbeats in 500ms at a 20ms interval — the turn is being held "+
					"open but the client is hearing nothing, which is how a working turn still ends "+
					"up looking dead", heartbeats)
			}
			if sim.Probes() == 0 {
				t.Fatal("the turn was held open without a single probe: it is being held open by " +
					"nothing more than the absence of a reason to close it")
			}
			return
		}
	}
}

// Scenario 3b — the same dropped stream, but the agent really did finish.
//
// The mirror image, and the reason 3 cannot simply be "never close on stream end": here provider
// truth says not-busy, so the turn must close rather than hang forever.
func TestSimDroppedStreamClosesWhenTheAgentIsActuallyDone(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.DroppedStream(false))
	go m.run()
	awaitState(t, frames, 3*time.Second, protocol.StatusIdle, protocol.StatusAbandoned)
}

// Scenario 4 — a wedged turn the provider swears is busy.
//
// Busy forever with no progress. The client must be kept patient (heartbeats, not a timeout), the
// nudges must be spent, and the end state must be needs_you: a human decision, not an agent error.
// Reporting this as an error is what teaches people to ignore the notification.
func TestSimWedgedTurnEndsAsNeedsYouNotError(t *testing.T) {
	m, sim, frames := simHarness(t, agentsim.WedgedBusy())
	go m.run()

	awaitState(t, frames, 3*time.Second, protocol.StatusStalled)
	ts := awaitState(t, frames, 3*time.Second, protocol.StatusNeedsYou, protocol.StatusError,
		protocol.StatusAbandoned)
	if ts.State != protocol.StatusNeedsYou {
		t.Fatalf("a turn the provider reports as BUSY ended as %q. Nothing failed — the agent is "+
			"wedged and a human has to look. Calling it an error pages someone about a bug that "+
			"does not exist.", ts.State)
	}
	if n := len(sim.Nudges()); n == 0 {
		t.Fatal("it gave up without trying to get the agent moving: zero nudges before paging a human")
	}
	if ts.Reason == "" {
		t.Fatal("needs_you with no reason — the notification says only that something needs you")
	}
}

// Scenario 4b — a user abort must work on a wedged turn.
//
// The one thing that must never be wedged is the way out.
func TestSimAWedgedTurnCanStillBeStopped(t *testing.T) {
	m, sim, frames := simHarness(t, agentsim.WedgedBusy())
	go m.run()
	awaitState(t, frames, 3*time.Second, protocol.StatusRunning, protocol.StatusStalled)

	if err := sim.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if sim.Stops() != 1 {
		t.Fatalf("Stop reached the provider %d times, want 1", sim.Stops())
	}
	awaitState(t, frames, 3*time.Second, protocol.StatusIdle, protocol.StatusNeedsYou,
		protocol.StatusAbandoned, protocol.StatusError)
}

// Scenario 7 — the provider is unreachable: probes are REFUSED, not slow.
//
// Absence, not latency. This is the one case that SHOULD end as abandoned, and it is why the
// unreachable window is separate from the slow window — a refusal is evidence, a timeout is not.
func TestSimUnreachableProviderIsAbandonedWithAReason(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.Unreachable())
	go m.run()

	ts := awaitState(t, frames, 3*time.Second, protocol.StatusAbandoned, protocol.StatusNeedsYou,
		protocol.StatusError, protocol.StatusIdle)
	// Specifically abandoned, and specifically NOT needs_you.
	//
	// The looser "any terminal state" version of this passed with the probe error swallowed — the
	// turn simply went stalled, spent its nudges and landed on needs_you, which satisfied the
	// assertion while proving nothing about unreachability. needs_you means a human can do
	// something; nobody can nudge a process that is not listening.
	if ts.State != protocol.StatusAbandoned {
		t.Fatalf("a provider REFUSING connections ended as %q, not abandoned. A refusal is evidence "+
			"of absence — routing it down the stall path asks a human to unstick an agent that is "+
			"not there.", ts.State)
	}
	if !strings.Contains(ts.Reason, "unreachable") {
		t.Fatalf("abandoned with reason %q, which does not say the agent was unreachable — the one "+
			"fact that distinguishes this from every other way a turn ends", ts.Reason)
	}
}

// Scenario 5 — fanout. The parent must not close while a child is open, and must close after.
func TestSimFanoutParentOutlivesItsChildren(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.Fanout(10))
	go m.run()

	var sawKids int
	// The running count from the most recent NON-terminal frame; see the assertion below for why
	// the terminal frame cannot answer this question.
	var lastRunningBeforeClose int
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-frames:
			ts, ok := decodeTurn(raw)
			if !ok {
				continue
			}
			running := 0
			for _, k := range ts.Children {
				if k.State == protocol.SubAgentRunning {
					running++
				}
			}
			if running > sawKids {
				sawKids = running
			}
			if ts.State != protocol.StatusIdle && ts.State != protocol.StatusError &&
				ts.State != protocol.StatusAbandoned && ts.State != protocol.StatusNeedsYou {
				lastRunningBeforeClose = running
			}
			if ts.State == protocol.StatusIdle {
				// Asserted on the frame BEFORE the close, not on the closing frame.
				//
				// closeTurn seals every unfinished child as done on its way out, and turnKids holds
				// POINTERS, so by the time the idle frame is built `running` is zero whatever
				// happened beforehand. Checking it there asserted a property the code could not
				// violate — the seal guaranteed the answer. The last pre-terminal frame is the one
				// the seal has not rewritten.
				if lastRunningBeforeClose > 0 {
					t.Fatalf("the parent turn closed while %d child(ren) were still running in the "+
						"frame immediately before — their output arrives after the turn the client "+
						"already rendered as finished", lastRunningBeforeClose)
				}
				if running > 0 {
					t.Fatalf("the closing frame itself still reports %d running children", running)
				}
				if sawKids < 2 {
					t.Fatalf("only ever saw %d concurrent children; this scenario spawns 10, so the "+
						"test would pass against a hub that tracked no children at all", sawKids)
				}
				return
			}
		case <-deadline:
			t.Fatalf("the fanout turn never closed (peak concurrent children seen: %d)", sawKids)
		}
	}
}

// Scenario 6 — a child's completion event is lost.
//
// One child never reports done. The parent must still close on provider truth rather than waiting
// forever on an orphan, because a lost child event is exactly as likely as a lost parent one.
func TestSimFanoutClosesWhenAChildsIdleIsLost(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.FanoutChildIdleLost(4))
	go m.run()
	awaitState(t, frames, 3*time.Second, protocol.StatusIdle, protocol.StatusNeedsYou,
		protocol.StatusAbandoned)
}

// Scenario 8 — a burst of deltas.
//
// Two properties under buffer pressure: the turn still closes, and every frame the client receives
// carries a seq that is strictly increasing. A gap here is a hole in the transcript that the cursor
// paging added in stage 4 cannot detect, because it trusts seq to be dense.
func TestSimBurstKeepsSeqMonotonic(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.Burst(2000))
	go m.run()

	var last int64
	var checked int
	deadline := time.After(5 * time.Second)
	for {
		select {
		case raw := <-frames:
			env, err := protocol.Decode(raw)
			if err != nil {
				continue
			}
			if env.Seq > 0 {
				if env.Seq <= last {
					t.Fatalf("seq went backwards under burst: %d after %d. Stage 4's cursor paging "+
						"reads seq as a total order, so this is a transcript that cannot be paged.",
						env.Seq, last)
				}
				last, checked = env.Seq, checked+1
			}
			if ts, ok := decodeTurn(raw); ok && ts.State == protocol.StatusIdle {
				if checked < 100 {
					t.Fatalf("only %d sequenced frames reached the client out of a 2000-delta burst "+
						"— this assertion is not exercising anything", checked)
				}
				return
			}
		case <-deadline:
			t.Fatalf("the burst turn never closed (%d sequenced frames seen)", checked)
		}
	}
}

// Every scenario, run for its shape rather than its specifics: a turn must not be left open forever
// and must not end in a state the client has no rendering for.
//
// This is the cheap net that catches a new state string, or a scenario combination nobody wrote an
// assertion for. It deliberately accepts any terminal state — the per-scenario tests above are where
// "which terminal state" is judged.
func TestSimEveryScenarioReachesATerminalState(t *testing.T) {
	terminal := map[string]bool{
		protocol.StatusIdle: true, protocol.StatusError: true,
		protocol.StatusNeedsYou: true, protocol.StatusAbandoned: true,
	}
	for _, sc := range []agentsim.Scenario{
		agentsim.HappyTurn(),
		agentsim.LostIdle(),
		agentsim.DroppedStream(false),
		agentsim.WedgedBusy(),
		agentsim.Unreachable(),
		agentsim.Fanout(3),
		agentsim.FanoutChildIdleLost(3),
		agentsim.Burst(200),
	} {
		t.Run(sc.Name, func(t *testing.T) {
			m, _, frames := simHarness(t, sc)
			go m.run()
			deadline := time.After(4 * time.Second)
			for {
				select {
				case raw := <-frames:
					ts, ok := decodeTurn(raw)
					if !ok {
						continue
					}
					if terminal[ts.State] {
						return
					}
					if ts.State != protocol.StatusRunning && ts.State != protocol.StatusStalled &&
						ts.State != protocol.StatusAwaitingApproval {
						t.Fatalf("turn.state = %q, which is not a state the client renders", ts.State)
					}
				case <-deadline:
					t.Fatal("the turn never reached a terminal state: the client spins forever")
				}
			}
		})
	}
}

// Scenario 6b — a child announces its own idle through the parent's stream.
//
// Sub-agent events carry the CHILD's session id and travel the parent's event stream. A hub that
// folded every status it saw into its own turn would end the parent the moment the first child
// finished, with its siblings still running and the parent's own answer still to come. The guard
// lives at the pump's routing boundary, so this drives real events rather than calling the turn
// machine directly — calling turnOnStatus by hand tests the wrong layer and passes either way.
func TestSimAChildsIdleDoesNotEndTheParentTurn(t *testing.T) {
	m, _, frames := simHarness(t, agentsim.ChildSpeaksIdle())
	go m.run()

	awaitState(t, frames, 3*time.Second, protocol.StatusRunning)
	deadline := time.After(400 * time.Millisecond)
	for {
		select {
		case raw := <-frames:
			ts, ok := decodeTurn(raw)
			if !ok {
				continue
			}
			if ts.State == protocol.StatusIdle || ts.State == protocol.StatusError ||
				ts.State == protocol.StatusAbandoned {
				t.Fatalf("a CHILD going idle closed the PARENT turn as %q. On a ten-way fanout the "+
					"first child to finish ends the whole turn, and every sibling's output arrives "+
					"after the client rendered it as done.", ts.State)
			}
		case <-deadline:
			return
		}
	}
}
