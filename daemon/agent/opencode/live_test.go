package opencode_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_RealOpenCodeApproval drives a full tool-approval round-trip against a
// real opencode server. The server must run in a directory configured to ASK for
// bash (opencode.json: {"permission":{"bash":"ask"}}). Opt-in:
//
//	OCULUS_OPENCODE_URL=http://127.0.0.1:PORT go test ./agent/opencode/ -run TestLive_RealOpenCodeApproval -v
func TestLive_RealOpenCodeApproval(t *testing.T) {
	base := os.Getenv("OCULUS_OPENCODE_URL")
	if base == "" {
		t.Skip("set OCULUS_OPENCODE_URL (server must have permission.bash=ask) to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	p := opencode.New(base)
	sess, err := p.Create(ctx, "", "Run the shell command: echo oculus-live-approval. Use the bash tool. Do nothing else.")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var gotApproval, gotIdle bool
	deadline := time.After(110 * time.Second)
	for !(gotApproval && gotIdle) {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("event stream closed early (approval=%v idle=%v)", gotApproval, gotIdle)
			}
			switch ev.Type {
			case protocol.TypeApprovalRequest:
				ar, ok := ev.Payload.(protocol.ApprovalRequest)
				if !ok {
					continue
				}
				gotApproval = true
				t.Logf("approval: tool=%q id=%q input=%s", ar.Tool, ar.ApprovalID, ar.Input)
				if err := sess.Respond(ctx, ar.ApprovalID, protocol.DecisionAllow); err != nil {
					t.Fatalf("respond: %v", err)
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
			t.Fatalf("timeout (approval=%v idle=%v)", gotApproval, gotIdle)
		}
	}
}

// TestLive_RealOpenCode drives a real `opencode serve` to validate that the
// provider parses live SSE into normalized events. Opt-in (invokes the LLM):
//
//	opencode serve --port 47822
//	OCULUS_OPENCODE_URL=http://127.0.0.1:47822 go test ./agent/opencode/ -run TestLive_RealOpenCode -v
//
// Skipped unless OCULUS_OPENCODE_URL is set.
func TestLive_RealOpenCode(t *testing.T) {
	base := os.Getenv("OCULUS_OPENCODE_URL")
	if base == "" {
		t.Skip("set OCULUS_OPENCODE_URL to run the live opencode test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	p := opencode.New(base)
	sess, err := p.Create(ctx, "", "Reply with exactly: hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var gotDelta, gotIdle bool
	deadline := time.After(80 * time.Second)
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
