package pi

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_PiProducesOutput drives a REAL pi and asserts the turn actually streams text.
//
// This is the regression guard for a bug that made pi look completely dead: pi blocks its run until
// every extension_ui_request receives an extension_ui_response, and its extensions open a turn by
// registering UI surfaces (setWidget "autoresearch"/"goal"/"subagent-async", setStatus). The adapter
// answered only confirm and select, so pi wedged before emitting a single token. The prompt was
// accepted and the turn closed idle with an empty reply — no error anywhere, just silence.
//
// A unit test against a fake pi-rpc cannot catch this: the fake only sends what we taught it to
// send, and we did not know real pi opened with widget requests. Hence a live test.
func TestLive_PiProducesOutput(t *testing.T) {
	bin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not installed")
	}

	p := New([]string{bin, "--mode", "rpc"})
	sess, err := p.Create(context.Background(), t.TempDir(), "Reply with exactly: OK")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var text strings.Builder
	sawIdle := false
	deadline := time.After(90 * time.Second)
	for !sawIdle {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				sawIdle = true
				break
			}
			switch pl := ev.Payload.(type) {
			case protocol.OutputDelta:
				text.WriteString(pl.Text)
			case protocol.SessionStatus:
				if pl.Status == protocol.StatusIdle || pl.Status == protocol.StatusError {
					sawIdle = true
				}
			}
		case <-deadline:
			t.Fatalf("pi never finished a turn; text so far = %q", text.String())
		}
	}

	// The precise wording is the model's business; that ANY assistant text arrived is the contract.
	// An empty string here is exactly the failure this test exists to catch — the turn completes
	// "successfully" while the user sees nothing.
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("turn completed but streamed no assistant text — pi is wedged on an unanswered extension_ui_request")
	}
}
