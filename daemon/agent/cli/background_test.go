package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// backgroundingAgent is an agent that leaves something running on purpose — a dev server, a watcher,
// a tailed log. The backgrounded process inherits the agent's stdout, so the write end of the pipe
// outlives the agent itself.
// The filler makes the output assertion below cover a real backlog (~190 KiB) rather than three
// lines. Measured honestly: it does NOT give that assertion teeth against the drain grace itself —
// closing the read end the instant cmd.Wait() returns still delivers every line, because the reader
// is already looping and drains the ~64 KiB the pipe can hold faster than waitpid returns. So treat
// the "last line survived" check as a guard against gross truncation, not as a negative control for
// streamDrainGrace. The grace is there for the case this fixture cannot stage — a slow consumer —
// and the assertion with a control behind it is the one about the turn ending at all.
const backgroundingAgent = `#!/bin/sh
echo "starting the dev server"
sh -c 'sleep 120' &
echo $! > "$OCULUS_TEST_PIDFILE"
i=0
while [ $i -lt 2048 ]; do
  echo "............................................................................................"
  i=$((i+1))
done
echo "the dev server is running in the background"
`

// The turn ends when the AGENT exits — not when the last holder of its stdout lets go.
//
// stream() used to run to EOF before cmd.Wait() was ever reached, which makes those two the same
// moment only if the agent forked nothing that outlives it. `npm run dev &` is enough: the
// grandchild holds the inherited write end, EOF never arrives, stream() blocks forever, Wait() is
// never called, `running` stays true — and Probe answers from `running` and is treated as
// authoritative liveness, so the turn engine's reconciler is told the session is healthy. The turn
// sits "working" until the daemon restarts, with no recovery path anywhere.
func TestTurnEndsWhenTheAgentExitsLeavingABackgroundedChild(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(script, []byte(backgroundingAgent), 0o755); err != nil {
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
	var output strings.Builder
	go func() {
		for ev := range sess.Events() {
			switch p := ev.Payload.(type) {
			case protocol.OutputDelta:
				output.WriteString(p.Text)
			case protocol.SessionStatus:
				if p.Status == protocol.StatusIdle {
					close(idle)
					return
				}
			}
		}
	}()

	if err := sess.Prompt(context.Background(), "start the dev server"); err != nil {
		t.Fatal(err)
	}
	pid := waitForPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	select {
	case <-idle:
	case <-time.After(20 * time.Second):
		t.Fatalf("the turn never ended. The agent exited seconds ago; its backgrounded child (pid %d) "+
			"still holds the inherited stdout, so stream() is blocked on a Read that will never EOF and "+
			"cmd.Wait() is never reached. Probe keeps answering `running == true`, which the turn "+
			"engine treats as authoritative, so nothing ever recovers this session.", pid)
	}

	// Not merely "it stopped waiting": everything the agent wrote must still have arrived, including
	// the line it wrote last. Losing that one is how a failed turn ends up with no explanation
	// anywhere (see exitHint). See the fixture comment for what this does and does not prove.
	if got := output.String(); !strings.Contains(got, "the dev server is running in the background") {
		t.Errorf("the agent's last line was dropped; output was %q", got)
	}
	if !alive(pid) {
		t.Errorf("the backgrounded child (pid %d) died, so this run proved nothing about a surviving "+
			"holder of the pipe", pid)
	}
}
