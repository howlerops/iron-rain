package opencode_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_OpencodeThreadTree drives a REAL opencode server.
//
// The endpoints here were read out of opencode's own OpenAPI document, which the pi work proved is a
// different question from how they behave: there, every shape assumption taken from the source was
// wrong on the wire (entryId not id, payload nested under data). So this asks the server.
//
//	OCULUS_OPENCODE_URL=http://127.0.0.1:PORT go test ./agent/opencode/ -run TestLive_OpencodeThreadTree -v
func TestLive_OpencodeThreadTree(t *testing.T) {
	base := os.Getenv("OCULUS_OPENCODE_URL")
	if base == "" {
		t.Skip("set OCULUS_OPENCODE_URL to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	p := opencode.New(base)
	sess, err := p.Create(ctx, t.TempDir(), "Reply with exactly: ONE")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	// Wait for the turn to finish so there is a user message to fork from.
	deadline := time.After(150 * time.Second)
	done := false
	for !done {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				done = true
				break
			}
			if st, ok := ev.Payload.(protocol.SessionStatus); ok {
				if st.Status == protocol.StatusIdle || st.Status == protocol.StatusError {
					done = true
				}
			}
		case <-deadline:
			t.Fatal("the turn never finished")
		}
	}

	ops, ok := sess.(agent.ThreadOps)
	if !ok {
		t.Fatal("opencode session does not implement ThreadOps")
	}
	nodes, err := ops.ThreadTree(ctx)
	if err != nil {
		t.Fatalf("ThreadTree: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("no fork points after a completed turn — the message list or the role filter is wrong")
	}
	for i, n := range nodes {
		if n.ID == "" {
			t.Errorf("node %d has no messageID, so neither fork nor revert can act on it", i)
		}
		if n.Preview == "" {
			t.Errorf("node %d would render as a blank row", i)
		}
	}
	current := 0
	for _, n := range nodes {
		if n.Current {
			current++
		}
	}
	if current != 1 {
		t.Errorf("%d nodes marked current, want exactly 1", current)
	}
	t.Logf("%d fork point(s); first=%q id=%q", len(nodes), nodes[0].Preview, nodes[0].ID)
}
