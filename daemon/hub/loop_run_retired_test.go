package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"

	"github.com/howlerops/oculus/daemon/loops"
	"github.com/howlerops/oculus/daemon/protocol"
)

// A finished loop session must retire its run, or the loop never fires again.
//
// loops.SetRunStatus is the only writer that retires a run, and it had exactly two callers in the
// tree — both inside its own package's tests. In production every Run row stayed "running" forever.
// The scheduler gates on activeRunCount() >= MaxConcurrent (default 1), so after a loop's FIRST run
// it silently skipped every later tick: ticket loops break, task loops continue, neither logs. The
// loop still reported Enabled and the deck still drew a live "running" chip, and the rows persist
// across restarts — so a recurring autonomous workflow fired exactly once, ever, while every surface
// said it was active.
//
// The loops package's own test passed throughout, because the TEST supplied the call production
// never made. This test deliberately does not: it drives the hub the way a real turn does and
// asserts the engine was told.
func TestFinishedLoopSessionRetiresItsRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   string
	}{
		{"a successful run", protocol.StatusIdle, "done"},
		{"a failed run", protocol.StatusError, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed a run that is still "running", through the engine's own persistence format — the
			// same rows a restart would reload, and the state every production run was stuck in.
			const sid = "sess_loop_1"
			path := filepath.Join(t.TempDir(), "loops.json")
			seed := `{"loops":[{"id":"lp1","name":"nightly","enabled":true,"kind":"task","max_concurrent":1}],` +
				`"runs":[{"loop_id":"lp1","session_id":"` + sid + `","status":"running"}]}`
			if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			eng := loops.New(path, nil, func() {})
			h := &Hub{loopEngine: eng, sessions: map[string]*managedSession{}}

			if got := runStatus(t, eng, sid); got != "running" {
				t.Fatalf("precondition: run status = %q, want \"running\"", got)
			}

			h.retireLoopRun(sid, tc.want)

			if got := runStatus(t, eng, sid); got != tc.want {
				t.Errorf("after %s the run is still %q, want %q — the loop's concurrency gate never "+
					"reopens, so it will never fire again", tc.status, got, tc.want)
			}
		})
	}
}

func runStatus(t *testing.T, eng *loops.Engine, sessionID string) string {
	t.Helper()
	for _, r := range eng.Runs() {
		if r.SessionID == sessionID {
			return r.Status
		}
	}
	t.Fatalf("no run recorded for %s", sessionID)
	return ""
}

// The same thing, driven through the PUMP rather than by calling the helper directly.
//
// This is the test that would actually have caught the bug: the defect was never that SetRunStatus
// was wrong, it was that no production path reached it. Asserting the helper forwards proves nothing
// if nothing calls the helper — so this drives a session to a terminal status the way a real turn
// does, and checks the engine was told.
func TestTerminalSessionStatusRetiresTheLoopRunThroughThePump(t *testing.T) {
	const sid = "sub_parent" // subSess.ID()
	path := filepath.Join(t.TempDir(), "loops.json")
	seed := `{"loops":[{"id":"lp1","name":"nightly","enabled":true,"kind":"task","max_concurrent":1}],` +
		`"runs":[{"loop_id":"lp1","session_id":"` + sid + `","status":"running"}]}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	eng := loops.New(path, nil, func() {})

	sess := &subSess{ch: make(chan agent.Event, 8)}
	h := &Hub{loopEngine: eng, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, sess, sessionMeta{loopName: "nightly"})
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: make(chan []byte, 64), done: make(chan struct{})}
	m.mu.Unlock()

	go m.run()
	for i := 0; i < 500 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.pumpAlive.Load() {
		t.Fatal("the pump never started")
	}

	sess.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: sid, Status: protocol.StatusRunning}}
	sess.ch <- agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: sid, Status: protocol.StatusIdle}}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runStatus(t, eng, sid) == "done" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the turn ended and the run is still %q — nothing on the terminal-status path retires it, "+
		"so the loop's concurrency gate stays shut and it never fires again", runStatus(t, eng, sid))
}
