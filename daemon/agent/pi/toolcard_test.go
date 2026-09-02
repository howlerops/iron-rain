package pi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// A fake `pi --mode rpc` reproducing the REAL two-frame tool card shape: tool_execution_start
// carries the toolName + args the summary is built from, and tool_execution_end carries only the
// output. The third frame is an end with no start — what a daemon restarted mid-turn sees.
const fakePiToolcardRPC = `#!/bin/sh
echo '{"type":"tool_execution_start","id":"t1","toolName":"bash","args":{"command":"npm test"}}'
echo '{"type":"tool_execution_end","id":"t1","output":"2 passed"}'
echo '{"type":"tool_execution_end","id":"ghost","output":"orphaned"}'
echo '{"type":"agent_end"}'
while IFS= read -r line; do :; done
`

// tool_execution_end is the state daemon/hub makes durable, and pi sends it with no title (and not
// always a toolName), so emitting it verbatim strips a card's command summary from history while
// leaving the live card on screen intact — the same defect the claude adapter had.
func TestPiToolCard_EndFrameKeepsNameAndTitle(t *testing.T) {
	sess := startFakePiToolcardSession(t)
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

	if running == nil || running.Name != "bash" || running.Title != "npm test" {
		t.Fatalf("running card = %+v, want Name=bash Title=%q", running, "npm test")
	}
	if completed.Name != "bash" || completed.Title != "npm test" {
		t.Fatalf("completed card = %+v, want the running card's Name/Title re-attached", *completed)
	}
	if completed.Output != "2 passed" || completed.Status != "completed" {
		t.Fatalf("completed card lost its own fields: %+v", *completed)
	}

	// No start frame means no identity to re-attach. Staying blank is correct; borrowing another
	// card's name would write a persistently WRONG label, which is worse than an empty one.
	if ghost.Name != "" || ghost.Title != "" {
		t.Fatalf("orphan end frame invented an identity: %+v", *ghost)
	}
	if ghost.Output != "orphaned" {
		t.Fatalf("orphan card lost its output: %+v", *ghost)
	}
}

// Delete-on-read is the only thing bounding the cache; without it a long-lived session accumulates
// one entry per tool call for as long as the daemon runs.
func TestPiToolCard_CacheIsEmptiedByTheEndFrame(t *testing.T) {
	sess := startFakePiToolcardSession(t)
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
				done = true // every tool frame has been processed by now
			}
		case <-timeout:
			t.Fatal("timeout waiting for the tool frames")
		}
	}

	s.toolMu.Lock()
	n := len(s.toolCards)
	s.toolMu.Unlock()
	if n != 0 {
		t.Fatalf("cache retained %d entries after the end frame, want 0", n)
	}
}

func startFakePiToolcardSession(t *testing.T) agent.Session {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi-toolcard.sh")
	if err := os.WriteFile(script, []byte(fakePiToolcardRPC), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{script}).Create(context.Background(), dir, "run the tests")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// A pi tool that FAILED must not render as a successful card.
//
// The adapter hardcoded ToolCompleted for every tool_execution_end, so a failure was drawn with a
// green checkmark carrying its own error text as output — the one status a user reads a tool card for
// was the one it could never report. pi's core emits isError on the event; we simply weren't reading
// it. Shapes that don't carry the flag are unchanged, rather than inventing a failure.
func TestFailedPiToolIsNotReportedAsCompleted(t *testing.T) {
	const failingRPC = `#!/bin/sh
echo '{"type":"tool_execution_start","id":"t1","toolName":"bash","args":{"command":"false"}}'
echo '{"type":"tool_execution_end","id":"t1","output":"exit status 1","isError":true}'
echo '{"type":"agent_end"}'
while IFS= read -r line; do :; done
`
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi-failing.sh")
	if err := os.WriteFile(script, []byte(failingRPC), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{script}).Create(context.Background(), dir, "run the tests")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var terminal *protocol.SessionTool
	timeout := time.After(10 * time.Second)
	for terminal == nil {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream ended before the tool reported a terminal status")
			}
			st, isTool := ev.Payload.(protocol.SessionTool)
			if !isTool || st.ID != "t1" || st.Status == protocol.ToolRunning {
				continue
			}
			terminal = &st
		case <-timeout:
			t.Fatal("timeout waiting for the tool's terminal frame")
		}
	}
	if terminal.Status != protocol.ToolError {
		t.Errorf("a failed tool ended as %q — it renders as a success carrying its own error text",
			terminal.Status)
	}
}

// pi's REAL tool wire, captured from a live `pi --mode rpc` session (0.80.2).
//
// tool_execution_start carries {toolCallId, toolName, args} and tool_execution_end carries
// {toolCallId, toolName, result, isError} — NOT the {id, output} this adapter was written against.
// pi renamed them upstream and nothing here noticed, because a missing JSON key decodes to "" rather
// than failing: every pi tool card was emitted with an EMPTY id and NO output. The client keys cards
// by id, so a whole turn's tools collapsed onto one blank card that never showed a result.
func TestPiRealWireToolFramesCarryIDAndOutput(t *testing.T) {
	const realRPC = `#!/bin/sh
echo '{"type":"tool_execution_start","toolCallId":"call_abc|fc_123","toolName":"bash","args":{"command":"echo hello"}}'
echo '{"type":"tool_execution_end","toolCallId":"call_abc|fc_123","toolName":"bash","result":{"content":[{"type":"text","text":"hello"}]},"isError":false}'
echo '{"type":"agent_end"}'
while IFS= read -r line; do :; done
`
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi-realwire.sh")
	if err := os.WriteFile(script, []byte(realRPC), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{script}).Create(context.Background(), dir, "run it")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var running, done *protocol.SessionTool
	timeout := time.After(10 * time.Second)
	for done == nil {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream ended before the tool completed")
			}
			st, isTool := ev.Payload.(protocol.SessionTool)
			if !isTool {
				continue
			}
			if st.Status == protocol.ToolRunning {
				cp := st
				running = &cp
			} else {
				cp := st
				done = &cp
			}
		case <-timeout:
			t.Fatal("timeout waiting for the tool frames")
		}
	}

	if running == nil || running.ID != "call_abc|fc_123" {
		t.Errorf("running card id = %q, want the toolCallId — an empty id collapses every card in the turn into one", idOf(running))
	}
	if done.ID != "call_abc|fc_123" {
		t.Errorf("completed card id = %q, want the toolCallId", done.ID)
	}
	if done.Output != "hello" {
		t.Errorf("output = %q, want the text from result.content — the card showed nothing", done.Output)
	}
	if done.Status != protocol.ToolCompleted {
		t.Errorf("status = %q, want completed", done.Status)
	}
}

func idOf(t *protocol.SessionTool) string {
	if t == nil {
		return "<no running frame>"
	}
	return t.ID
}
