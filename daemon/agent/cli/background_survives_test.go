package cli

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A process the agent deliberately BACKGROUNDED must survive the turn ending.
//
// The fix for "a backgrounded grandchild wedges the turn forever" ends the turn when the agent
// exits, then stopped waiting on the pipe. The first version stopped waiting by CLOSING the read
// end — which delivers SIGPIPE to that grandchild on its next write and kills it. So the turn
// stopped hanging and started killing the `npm run dev &` it was hanging on, about a second later:
// the same bug wearing a different coat, and worse, because the user asked for that server.
//
// Verified mechanically before this test existed: a child writing into a pipe whose read end was
// closed exits `signal: broken pipe`.
const chattyBackgroundAgent = `#!/bin/sh
sh -c 'i=0; while [ $i -lt 40 ]; do echo "dev server tick $i"; sleep 0.1; i=$((i+1)); done' &
echo $! > "$OCULUS_TEST_PIDFILE"
echo "started the dev server"
`

func TestABackgroundedProcessSurvivesTheTurnEnding(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(script, []byte(chattyBackgroundAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child.pid")
	t.Setenv("OCULUS_TEST_PIDFILE", pidFile)

	sess, err := NewProvider(Config{Name: "faker", Command: script, Args: []string{"{prompt}"}}).
		Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	idle := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			if st, ok := ev.Payload.(protocol.SessionStatus); ok && st.Status == protocol.StatusIdle {
				close(idle)
				return
			}
		}
	}()
	if err := sess.Prompt(context.Background(), "start it"); err != nil {
		t.Fatal(err)
	}
	pid := waitForPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	select {
	case <-idle:
	case <-time.After(20 * time.Second):
		t.Fatal("the turn never ended")
	}

	// The turn is over. The backgrounded process is still writing into the pipe — and must still be
	// alive well after the drain was cut.
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			t.Fatalf("the backgrounded process (pid %d) was KILLED when the turn ended.\n\n"+
				"Closing the read end of its stdout sends it SIGPIPE on the next write. The whole "+
				"point of ending the turn on agent exit is to support an agent that leaves something "+
				"running — killing that something a second later is the same defect inverted.", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
