package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// oversizedFrame is one JSONL line past pi's 16 MB scanner cap — a large tool result — followed by
// frames that should never be reached, and a child that stays alive afterwards.
func oversizedFrame(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	big := filepath.Join(dir, "frame.jsonl")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","output":"`); err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("A", 1<<20)
	for i := 0; i < 17; i++ { // 17 MiB of payload, against a 16 MiB cap
		if _, err := f.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.WriteString("\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"before"}}'
cat %q
echo '{"type":"agent_end"}'
sleep 120
`, big)
}

// An oversized frame must end the session, not wedge it.
//
// Two defects met here. pi's readLoop never checked sc.Err(), so a truncated stream returned the
// stale `idle` flag and the caller's `if !sawIdle && !shuttingDown` backstop was skipped — everything
// after the big line discarded in silence. And with the scanner gone, nobody drained stdout: the
// child stayed parked in write(), so cmd.Wait() never returned and closeEvents() was never reached.
// The session's event stream stayed open forever on a process that would never speak again, and
// Probe — which answers from the live child — kept reporting it healthy, so the turn engine's
// reconciler had nothing to act on.
func TestOversizedFrameEndsTheSessionInsteadOfWedgingIt(t *testing.T) {
	sess := spawnScript(t, oversizedFrame(t))

	var sawStreamError bool
	deadline := time.After(60 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				// The channel closing is the whole point: it means cmd.Wait() returned, which means
				// the child was actually reaped.
				if !sawStreamError {
					t.Error("the stream was truncated mid-frame and nothing was reported. pi's readLoop " +
						"never checked sc.Err(), so every frame after the oversized line was dropped " +
						"silently and the turn was reported as a normal ending.")
				}
				return
			}
			if st, isStatus := ev.Payload.(protocol.SessionStatus); isStatus {
				if st.Status == protocol.StatusError && strings.Contains(st.Detail, "lost the agent's output stream") {
					sawStreamError = true
				}
				if st.Status == protocol.StatusIdle {
					t.Errorf("a stream that died mid-frame was reported as a normally finished turn (idle)")
				}
			}
		case <-deadline:
			t.Fatal("the events channel never closed: cmd.Wait() is still blocked on a child parked " +
				"writing into a pipe with no reader. The session can never end, the hub keeps its turn " +
				"'working' forever, and the pi process and everything it forked leak.")
		}
	}
}
