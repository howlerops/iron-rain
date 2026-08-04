package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
)

// shutdownSess records whether Close ran and can be made to hang, which is the two things Shutdown
// has to get right: reach every session, and never depend on any one of them cooperating.
type shutdownSess struct {
	id      string
	events  chan agent.Event
	closes  atomic.Int32
	hangFor time.Duration
	panics  bool
}

func (s *shutdownSess) ID() string                                    { return s.id }
func (s *shutdownSess) Provider() string                              { return "fake" }
func (s *shutdownSess) Events() <-chan agent.Event                    { return s.events }
func (s *shutdownSess) Prompt(context.Context, string) error          { return nil }
func (s *shutdownSess) Respond(context.Context, string, string) error { return nil }
func (s *shutdownSess) Stop(context.Context) error                    { return nil }

func (s *shutdownSess) Close() error {
	s.closes.Add(1)
	if s.panics {
		panic("provider blew up in Close")
	}
	if s.hangFor > 0 {
		time.Sleep(s.hangFor)
	}
	return nil
}

// TestShutdownClosesEverySession is the graceful half of the sidecar-leak fix. Shutdown used to stop
// only the LSP manager, so a daemon stopped cleanly (launchctl kickstart, logout, a self-update
// restart) left every agent child running: procutil.Isolate deliberately puts each harness in its own
// process group, so nothing signals them when the daemon goes and they are simply reparented to
// launchd. Closing each session is the only thing that ends them. 143 orphans, 12.7 GB, is what this
// test protects against.
func TestShutdownClosesEverySession(t *testing.T) {
	h := New()
	var sessions []*shutdownSess
	for _, id := range []string{"a", "b", "c"} {
		s := &shutdownSess{id: id, events: make(chan agent.Event)}
		sessions = append(sessions, s)
		manage(t, h, s)
	}

	h.Shutdown()

	for _, s := range sessions {
		if got := s.closes.Load(); got != 1 {
			t.Errorf("session %s: Close called %d times, want exactly 1 — an unclosed session is a leaked process tree", s.id, got)
		}
	}
}

// TestShutdownIsBoundedByOneWedgedSession: a provider whose Close blocks (a pipe write to a process
// that stopped reading) must not hold the daemon open. Whoever is stopping us escalates to SIGKILL,
// and a SIGKILLed daemon orphans every child — the exact outcome the sweep exists to prevent, caused
// by the sweep itself.
func TestShutdownIsBoundedByOneWedgedSession(t *testing.T) {
	h := New()
	wedged := &shutdownSess{id: "wedged", events: make(chan agent.Event), hangFor: 30 * time.Second}
	fine := &shutdownSess{id: "fine", events: make(chan agent.Event)}
	manage(t, h, wedged)
	manage(t, h, fine)

	start := time.Now()
	h.closeSessions(200 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("shutdown waited %s on a wedged provider — the budget is not being honoured", elapsed)
	}
	// The healthy session must still have been reaped: closes run in parallel, so one hang cannot
	// starve the rest.
	if fine.closes.Load() != 1 {
		t.Error("a wedged session blocked an unrelated session's Close — the sweep must be parallel")
	}
}

// TestShutdownSurvivesAPanickingProvider: if one provider panics in Close, the sessions we hadn't
// reached yet would become exactly the orphans this whole change exists to prevent.
func TestShutdownSurvivesAPanickingProvider(t *testing.T) {
	h := New()
	bad := &shutdownSess{id: "bad", events: make(chan agent.Event), panics: true}
	good := &shutdownSess{id: "good", events: make(chan agent.Event)}
	manage(t, h, bad)
	manage(t, h, good)

	h.Shutdown() // must not take the process down with it

	if good.closes.Load() != 1 {
		t.Error("a panicking provider prevented another session from being closed")
	}
}

// TestShutdownIsSafeWithNoSessions guards the ordinary case — a daemon stopped before anything ran.
func TestShutdownIsSafeWithNoSessions(t *testing.T) {
	h := New()
	h.Shutdown()
	if n := h.closeSessions(time.Second); n != 0 {
		t.Errorf("closeSessions reported %d sessions on an empty hub", n)
	}
}

// TestShutdownSilencesNotifications: a session with an open turn ends as an "abandoned" turn, which
// legitimately pages the user that the agent stopped responding. On a planned shutdown that is a
// false alarm about a failure the daemon itself caused, and it would fire on every self-update
// restart, once per busy session.
func TestShutdownSilencesNotifications(t *testing.T) {
	h := New()
	h.mu.Lock()
	h.pushTokens = []string{"token"}
	h.mu.Unlock()

	h.Shutdown()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifier != nil || h.slack != nil || len(h.pushTokens) != 0 || h.activity != nil {
		t.Error("Shutdown must detach the alert sinks before tearing sessions down")
	}
}

// TestCloseSessionsSnapshotsUnderLock: Close ends a provider's event stream, whose run() goroutine
// calls back into the hub (detachSession) and mutates h.sessions. Holding h.mu across the closes
// would deadlock, so the sweep must snapshot first — this fails by hanging if that regresses.
func TestCloseSessionsSnapshotsUnderLock(t *testing.T) {
	h := New()
	s := &shutdownSess{id: "reentrant", events: make(chan agent.Event)}
	manage(t, h, s)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Mirrors what run() does as the stream ends, concurrently with the sweep.
		h.detachSession("reentrant", nil)
	}()
	h.closeSessions(2 * time.Second)
	wg.Wait()
}
