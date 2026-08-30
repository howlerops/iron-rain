package pi

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_PiForkPoints drives a REAL pi and asserts the fork plumbing works end to end.
//
// Live rather than against a fake, for the same reason the output test is: the fake only sends what
// we taught it to, and what we needed to learn here was the SHAPE of pi's answers — that
// get_fork_messages returns user messages under a "messages" key, that responses are correlated by
// command name with no id to echo, and that the tree the TUI draws is not reachable from rpc mode at
// all. Every one of those was discovered by asking the real thing.
func TestLive_PiForkPoints(t *testing.T) {
	bin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not installed")
	}

	p := New([]string{bin, "--mode", "rpc"})
	sess, err := p.Create(context.Background(), t.TempDir(), "Reply with exactly: ONE")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	// Drain until the first turn finishes, so there is a user message to fork from.
	waitIdle(t, sess, 90*time.Second)

	ops, ok := sess.(agent.ThreadOps)
	if !ok {
		t.Fatal("the pi session does not implement ThreadOps")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nodes, err := ops.ThreadTree(ctx)
	if err != nil {
		t.Fatalf("ThreadTree: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("no fork points after a completed turn — get_fork_messages returned nothing")
	}
	// Every node must be usable by a picker: an id to act on and something to read.
	for i, n := range nodes {
		if n.ID == "" {
			t.Errorf("node %d has no id, so it cannot be forked from", i)
		}
		if strings.TrimSpace(n.Preview) == "" {
			t.Errorf("node %d has an empty preview — the picker would show a blank row", i)
		}
	}
	// Exactly one node is the session's current position.
	current := 0
	for _, n := range nodes {
		if n.Current {
			current++
		}
	}
	if current != 1 {
		t.Errorf("%d nodes marked current, want exactly 1", current)
	}

	// Rewind is declared FALSE for this adapter and must refuse rather than silently fork — a fork
	// dressed as a rewind loses the branch summary and the tree without saying so.
	if err := ops.ThreadRewind(ctx, nodes[0].ID); err == nil {
		t.Error("ThreadRewind must refuse over rpc, not fall back to forking")
	}
}

// waitIdle drains a session's events until it goes idle or the deadline passes.
func waitIdle(t *testing.T, sess agent.Session, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return
			}
			if pl, ok := ev.Payload.(protocol.SessionStatus); ok {
				if pl.Status == protocol.StatusIdle || pl.Status == protocol.StatusError {
					return
				}
			}
		case <-deadline:
			t.Fatal("pi never finished its first turn")
		}
	}
}
