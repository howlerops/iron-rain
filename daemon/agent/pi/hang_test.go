package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/procutil"
)

// mutePi never reads its stdin at all, so the 64 KiB pipe fills and stays full — the state a pi
// wedged inside a tool call (or stopped under a debugger) leaves behind.
const mutePi = `#!/bin/sh
sleep 120
`

// Every method that takes a context must honour it.
//
// pi's send() took no context at all and each method named its own `_`, so the 15s deadline
// heartbeat.go's promptBounded exists to impose was discarded. heartbeatTick walks sessions SERIALLY
// from one ticker goroutine: a Prompt parked in a write to a full pipe therefore stopped budget
// enforcement, handoff indexing and stall detection for every session behind it — permanently, and
// silently, since nothing timed out to log.
func TestPromptHonoursItsDeadlineWhenPiStopsReadingStdin(t *testing.T) {
	sess := spawnScript(t, mutePi)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	// 256 KiB against a 64 KiB pipe nobody drains.
	go func() { done <- sess.Prompt(ctx, strings.Repeat("x", 256*1024)) }()

	select {
	case err := <-done:
		if err == nil {
			t.Skip("the write completed — this platform buffered the whole prompt, so there was " +
				"never a blocked writer to bound")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Prompt ignored its 500ms deadline and is still parked in a write to pi's stdin.\n\n" +
			"heartbeat.go wraps this call in a 15s bound precisely so one provider cannot stall the " +
			"serial heartbeat tick. Discarding it stops budget enforcement, handoff indexing and " +
			"stall detection for every session after this one, with nothing logged.")
	}
}

// Stop is the same obligation and matters more: it is what the user reaches for when the session is
// already misbehaving, and it parks the connection goroutine carrying `session.stop`.
func TestStopHonoursItsDeadlineWhenPiStopsReadingStdin(t *testing.T) {
	sess := spawnScript(t, mutePi)

	// Park a writer first so writeMu is held and the abort cannot reach the pipe either. Deliberately
	// unbounded and never joined: it is the hostage, not the subject, and waiting on it here would
	// make the test hang in its own setup instead of reporting what it found.
	filled := make(chan struct{})
	go func() { defer close(filled); _ = sess.Prompt(context.Background(), strings.Repeat("x", 256*1024)) }()
	time.Sleep(500 * time.Millisecond) // long enough to claim writeMu and block in Write
	select {
	case <-filled:
		t.Skip("the filling write completed — no parked writer, so there is nothing to block against")
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sess.Stop(ctx) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Stop ignored its deadline behind a parked writer — interrupting a wedged pi session " +
			"hangs the connection goroutine and never answers the client.")
	}
}

// spawnScript starts a session on a throwaway shell script and guarantees the tree is killed.
func spawnScript(t *testing.T, body string) *session {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pi.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
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
	return s
}
