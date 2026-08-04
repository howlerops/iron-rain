package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// A fake sidecar that reproduces the sidecar's REAL two-frame tool card shape: the tool_use frame
// carries {tool, detail} and the matching tool_result frame carries only {id, output, status}. The
// third frame is a terminal-only card with no start — what a daemon restarted mid-turn sees.
const fakeToolcardSidecar = `#!/bin/sh
echo '{"t":"session","id":"'"$OCULUS_SESSION_ID"'"}'
echo '{"t":"toolcall","id":"t1","tool":"Bash","detail":"npm test","status":"running"}'
echo '{"t":"toolcall","id":"t1","output":"2 passed","status":"completed"}'
echo '{"t":"toolcall","id":"ghost","output":"orphaned","status":"completed"}'
echo '{"t":"idle"}'
while IFS= read -r line; do :; done
`

// The terminal tool frame is the ONLY state daemon/hub makes durable, so if it goes out with an
// empty Name/Title the card survives on screen (the app merges onto the live card) but comes back
// from history as an anonymous, untitled box. This test asserts on the terminal frame specifically
// — asserting on the running frame would have passed all along and caught nothing.
func TestToolCard_TerminalFrameKeepsNameAndTitle(t *testing.T) {
	sess := startFakeToolcardSession(t)
	defer sess.Close()

	var running, completed, ghost *protocol.SessionTool
	timeout := time.After(10 * time.Second)
	for completed == nil || ghost == nil {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("stream ended before all tool frames arrived (running=%t completed=%t ghost=%t)",
					running != nil, completed != nil, ghost != nil)
			}
			st, isTool := ev.Payload.(protocol.SessionTool)
			if ev.Type != protocol.TypeSessionTool || !isTool {
				continue
			}
			switch {
			case st.ID == "t1" && st.Status == "running":
				running = &st
			case st.ID == "t1":
				completed = &st
			case st.ID == "ghost":
				ghost = &st
			}
		case <-timeout:
			t.Fatalf("timeout (running=%t completed=%t ghost=%t)", running != nil, completed != nil, ghost != nil)
		}
	}

	if running == nil || running.Name != "Bash" || running.Title != "npm test" {
		t.Fatalf("running card = %+v, want Name=Bash Title=%q", running, "npm test")
	}
	if completed.Name != "Bash" || completed.Title != "npm test" {
		t.Fatalf("completed card = %+v, want the running card's Name/Title re-attached", *completed)
	}
	if completed.Output != "2 passed" || completed.Status != "completed" {
		t.Fatalf("completed card lost its own fields: %+v", *completed)
	}

	// A terminal frame with no prior running frame must stay honestly blank rather than borrowing
	// some other card's identity — a wrong name on a persisted card is worse than no name.
	if ghost.Name != "" || ghost.Title != "" {
		t.Fatalf("orphan terminal card invented an identity: %+v", *ghost)
	}
	if ghost.Output != "orphaned" {
		t.Fatalf("orphan card lost its output: %+v", *ghost)
	}
}

// The cache is bounded ONLY by delete-on-read: a session that stays up for days runs thousands of
// tools, so an entry that outlives its terminal frame is a real leak in a long-running daemon.
func TestToolCard_CacheIsEmptiedByTheTerminalFrame(t *testing.T) {
	sess := startFakeToolcardSession(t)
	defer sess.Close()

	s, ok := sess.(*session)
	if !ok {
		t.Fatalf("Create returned %T, want *session", sess)
	}

	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				done = true
				continue
			}
			if st, isTool := ev.Payload.(protocol.SessionTool); isTool && st.ID == "ghost" {
				done = true // every toolcall frame has been processed by now
			}
		case <-timeout:
			t.Fatal("timeout waiting for the tool frames")
		}
	}

	s.toolMu.Lock()
	n := len(s.toolCards)
	s.toolMu.Unlock()
	if n != 0 {
		t.Fatalf("cache retained %d entries after the terminal frame, want 0", n)
	}
}

func startFakeToolcardSession(t *testing.T) agent.Session {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-toolcard-sidecar.sh")
	if err := os.WriteFile(script, []byte(fakeToolcardSidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{script}).Create(context.Background(), dir, "run the tests")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}
