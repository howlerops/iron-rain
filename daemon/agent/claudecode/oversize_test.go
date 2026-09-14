package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/howlerops/oculus/daemon/procutil"
	"github.com/howlerops/oculus/daemon/protocol"
)

// oversizedSidecar emits one frame past the 8 MB scanner cap — a large tool result is all it takes —
// and then stays alive, as the real sidecar does.
func oversizedSidecar(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	big := filepath.Join(dir, "frame.jsonl")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"t":"text","text":"`); err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("A", 1<<20)
	for i := 0; i < 9; i++ { // 9 MiB of payload, against an 8 MiB cap
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
echo '{"t":"session","id":"11111111-2222-3333-4444-555555555555"}'
cat %q
echo '{"t":"idle"}'
sleep 120
`, big)
}

// A frame over the cap must end the session, not strand it.
//
// readLoop correctly gives up on a broken stream — but the sidecar does not stop writing. It stays
// parked in write(), pushing the rest of that frame into a 64 KiB pipe that no longer has a reader,
// so it never exits, cmd.Wait() never returns, and closeEvents() is never reached. The events channel
// stays open forever, the hub keeps the turn "working", the node tree leaks, and Probe — answered by
// the sidecar, which is still perfectly alive — tells the reconciler there is nothing to recover.
//
// procutil's WaitDelay does not cover it: that timer starts only once the ctx is cancelled or Wait
// has already seen the process exit, and here neither ever happens.
func TestOversizedFrameReapsTheSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar.sh")
	if err := os.WriteFile(path, []byte(oversizedSidecar(t)), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{path}).Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	s := sess.(*session)
	t.Cleanup(func() {
		_ = s.Close()
		procutil.TerminateGroup(s.cmd)
	})

	var sawStreamError bool
	deadline := time.After(60 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				// The close is the assertion: it only happens after cmd.Wait() returns, i.e. after the
				// sidecar has actually been reaped.
				if !sawStreamError {
					t.Error("the session ended without reporting the lost stream")
				}
				return
			}
			if st, isStatus := ev.Payload.(protocol.SessionStatus); isStatus {
				if st.Status == protocol.StatusError && strings.Contains(st.Detail, "lost the agent's output stream") {
					sawStreamError = true
				}
			}
		case <-deadline:
			t.Fatal("the events channel never closed: cmd.Wait() is still blocked on a sidecar parked " +
				"writing into a pipe with no reader. The session can never end, its turn stays " +
				"'working' forever, and the sidecar plus its `claude` child leak.")
		}
	}
}
