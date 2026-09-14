package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// muteSidecar announces its session and then never reads stdin at all. The 64 KiB pipe therefore
// fills and stays full — the state a wedged sidecar leaves behind.
//
// This is deliberately NOT deafSidecar: that one still drains stdin (`while read line; do :; done`),
// so a write into it always completes and the deadlock below can never occur. The comment there
// claims the sidecar is "wedged so hard its stdin loop is gone"; the script says otherwise, which is
// why this case went uncovered.
const muteSidecar = `#!/bin/sh
echo '{"t":"session","id":"'"$OCULUS_SESSION_ID"'"}'
sleep 120
`

// Close() must reap the sidecar even when a writer is parked in stdin.Write.
//
// A prompt larger than the pipe parks its goroutine inside Write holding writeMu, and sendCtx
// releases only the CALLER when the context expires — the writer stays put until the pipe drains or
// the process dies. Close used to take writeMu before TerminateGroup, so it waited on the very thing
// the kill would have fixed: stop/delete hung the connection goroutine with no reply, and on
// shutdown the sidecar and its `claude` child outlived the daemon.
func TestCloseReapsASidecarThatStoppedReadingStdin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar.sh")
	if err := os.WriteFile(path, []byte(muteSidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{path}).Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}

	// Fill the pipe: 256 KiB against a 64 KiB buffer nobody is draining.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	perr := sess.Prompt(ctx, strings.Repeat("x", 256*1024))
	if perr == nil {
		t.Skip("the write completed — this platform buffered the whole prompt, so there is no " +
			"parked writer to deadlock against")
	}

	done := make(chan error, 1)
	go func() { done <- sess.Close() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Close() deadlocked on writeMu, which a parked stdin writer holds.\n\n" +
			"TerminateGroup — the only thing that would free that writer — sits after the lock, so " +
			"it is never reached: stopping or deleting the session hangs the connection goroutine, " +
			"and on shutdown the sidecar and its `claude` child outlive the daemon.")
	}
}

// Probe carries its caller's deadline. The hub calls it from the per-turn goroutine with a 10s
// bound; discarding that bound parked the goroutine that is supposed to decide whether the session
// is alive, so the turn never heartbeated, nudged or reconciled — the exact failure Probe exists to
// detect.
func TestProbeHonoursItsDeadlineWhenStdinIsFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar.sh")
	if err := os.WriteFile(path, []byte(muteSidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{path}).Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	fill, cancelFill := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelFill()
	if err := sess.Prompt(fill, strings.Repeat("x", 256*1024)); err == nil {
		t.Skip("the write completed — no parked writer on this platform")
	}

	p, ok := sess.(interface {
		Probe(context.Context) (bool, error)
	})
	if !ok {
		t.Fatal("claude-code session no longer implements agent.Prober")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = p.Probe(ctx) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Probe ignored its context and blocked in the write — the turn goroutine that calls " +
			"it is now stuck, so nothing can contradict a session that claims to be working")
	}
}
