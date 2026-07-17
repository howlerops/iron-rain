package claudecode_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/claudecode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_RealClaudeCode drives the real Node sidecar (Agent SDK) against real
// claude-code to validate the streaming session end to end. Opt-in (invokes the LLM):
//
//	OCULUS_CLAUDE_SIDECAR=/abs/sidecar/sidecar.mjs go test ./agent/claudecode/ -run TestLive_RealClaudeCode -v
//
// Skipped unless OCULUS_CLAUDE_SIDECAR is set.
func TestLive_RealClaudeCode(t *testing.T) {
	sidecar := os.Getenv("OCULUS_CLAUDE_SIDECAR")
	if sidecar == "" {
		t.Skip("set OCULUS_CLAUDE_SIDECAR (path to sidecar.mjs) to run the live claude-code test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	p := claudecode.New([]string{"node", sidecar})
	sess, err := p.Create(ctx, "", "Reply with exactly: hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var gotDelta, gotIdle bool
	deadline := time.After(110 * time.Second)
	for !(gotDelta && gotIdle) {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("event stream closed early (delta=%v idle=%v)", gotDelta, gotIdle)
			}
			switch ev.Type {
			case protocol.TypeOutputDelta:
				if d, ok := ev.Payload.(protocol.OutputDelta); ok && d.Text != "" {
					gotDelta = true
					t.Logf("delta: %q", d.Text)
				}
			case protocol.TypeSessionStatus:
				if s, ok := ev.Payload.(protocol.SessionStatus); ok {
					t.Logf("status: %s", s.Status)
					if s.Status == protocol.StatusIdle || s.Status == protocol.StatusDone {
						gotIdle = true
					}
				}
			}
		case <-deadline:
			t.Fatalf("timeout (delta=%v idle=%v)", gotDelta, gotIdle)
		}
	}
}
