package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// forkingSidecar models the process shape that actually leaked: a sidecar that forks a long-lived
// child (the `claude` process the Agent SDK spawns) and then, like every sidecar built before the EOF
// handler existed, refuses to die when its stdin closes. Closing the session has to reap BOTH.
const forkingSidecar = `#!/bin/sh
sleep 300 &
echo $! > "$OCULUS_TEST_PIDFILE"
echo '{"t":"session","id":"'"$OCULUS_SESSION_ID"'"}'
echo '{"t":"idle"}'
while IFS= read -r line; do :; done
sleep 300
`

func aliveTest(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitGoneTest(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !aliveTest(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !aliveTest(pid)
}

// TestCloseReapsTheWholeSidecarTree is the graceful-path guarantee. Close used to close stdin and
// cancel the context, and cancellation kills only the DIRECT child — so the `claude` process the
// sidecar had spawned survived, reparented to launchd, invisible to the daemon that started it. This
// is what turned ordinary session teardown into 284 stray processes on a real machine.
func TestCloseReapsTheWholeSidecarTree(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "forking-sidecar.sh")
	if err := os.WriteFile(script, []byte(forkingSidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "grandchild.pid")
	t.Setenv("OCULUS_TEST_PIDFILE", pidFile)

	p := New([]string{script})
	sess, err := p.Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the forked "claude" to report its pid.
	var gpid int
	for i := 0; i < 300; i++ {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				gpid = n
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gpid == 0 {
		_ = sess.Close()
		t.Fatal("the fake sidecar never forked its child")
	}
	if !aliveTest(gpid) {
		_ = sess.Close()
		t.Fatal("the forked child should be running before Close")
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !waitGoneTest(gpid, 5*time.Second) {
		_ = syscall.Kill(gpid, syscall.SIGKILL) // don't leak a 300s sleeper out of a failing test
		t.Fatal("THE SIDECAR'S CHILD SURVIVED Close — the process-group reap is not reaching the tree")
	}
	_ = sess.Close() // idempotent: shutdown races call this concurrently with the session's own teardown
}
