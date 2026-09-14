package hub

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// panicSess panics from Provider() once armed — the pump calls it while logging a turn, so this
// reproduces "a third-party harness made the daemon's own code blow up mid-event".
type panicSess struct {
	ch    chan agent.Event
	armed atomic.Bool
}

func (s *panicSess) ID() string { return "sess_panic" }
func (s *panicSess) Provider() string {
	if s.armed.Load() {
		panic("provider exploded")
	}
	return "fake"
}
func (s *panicSess) Events() <-chan agent.Event                    { return s.ch }
func (s *panicSess) Prompt(context.Context, string) error          { return nil }
func (s *panicSess) Respond(context.Context, string, string) error { return nil }
func (s *panicSess) Stop(context.Context) error                    { return nil }
func (s *panicSess) Close() error                                  { return nil }

// A panic in one session's pump must not take the daemon — or any other session — with it.
//
// The pump runs once per session and parses whatever a third-party harness chose to send, and a
// panic in ANY goroutine ends the Go process. So one malformed frame from one provider killed the
// whole daemon: every other session died with it, the app showed a dead connection with no reason,
// and the only evidence was a stack trace in a log file the user has never heard of.
//
// If this regresses, the test binary itself crashes rather than failing — which is exactly the
// blast radius being asserted.
func TestPanicInOneSessionDoesNotKillTheDaemon(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	h := &Hub{db: db, sessions: map[string]*managedSession{}}

	// A healthy session that must still be alive at the end.
	healthy := &subSess{ch: make(chan agent.Event, 8)}
	hm := newManagedSession(h, healthy, sessionMeta{})
	go hm.run()

	bad := &panicSess{ch: make(chan agent.Event, 8)}
	m := newManagedSession(h, bad, sessionMeta{})
	frames := make(chan []byte, 64)
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()
	go m.run()

	for i := 0; i < 500 && !(m.pumpAlive.Load() && hm.pumpAlive.Load()); i++ {
		time.Sleep(2 * time.Millisecond)
	}

	// Arm it, then send a status the pump will LOG — the log line calls Provider(), which panics
	// from inside the pump's own goroutine. Without containment this ends the test binary.
	bad.armed.Store(true)
	bad.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: "sess_panic", Status: protocol.StatusRunning}}

	// The daemon is still here (we are still executing), and the healthy session's pump is untouched.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hm.pumpAlive.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hm.pumpAlive.Load() {
		t.Fatal("an unrelated session's pump died — the blast radius was not contained")
	}

	// And prove it still PROCESSES events, not merely that its goroutine exists.
	healthy.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: healthy.ID(), Status: protocol.StatusIdle}}
	ok := false
	for i := 0; i < 300 && !ok; i++ {
		hm.mu.Lock()
		ok = hm.lastStatus == protocol.StatusIdle
		hm.mu.Unlock()
		if !ok {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !ok {
		t.Error("the healthy session stopped processing events after another session panicked")
	}
}

// A panicked session must be torn DOWN, not left bound to a dead pump.
//
// The recover above returns normally, which used to skip every line after the pump loop — including
// the detachSession/removeSession that ends the binding. So the hub kept the session in h.sessions
// with nothing listening on the provider behind it: its pending approvals could never be answered
// (the answer travels through that pump), its MCP token stayed valid, and every session list kept
// offering a row that responds to nothing. The user's only recovery was restarting the daemon.
func TestAPanickedSessionIsUnboundFromTheHub(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	bad := &panicSess{ch: make(chan agent.Event, 8)}
	m := newManagedSession(h, bad, sessionMeta{})
	h.mu.Lock()
	h.sessions["sess_panic"] = m
	h.mu.Unlock()
	go m.run()

	for i := 0; i < 500 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}

	bad.armed.Store(true)
	bad.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: "sess_panic", Status: protocol.StatusRunning}}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		_, still := h.sessions["sess_panic"]
		h.mu.Unlock()
		if !still {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the session is still bound in h.sessions after its pump panicked.\n\n" +
		"Nothing is reading the provider any more, but the hub still offers the session: its " +
		"approvals can never be resolved, its MCP token is never revoked, and the row stays in every " +
		"session list answering nothing until the daemon is restarted.")
}
