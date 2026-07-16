package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A fake `claude` that emits stream-json output, then simulates a PreToolUse hook
// by POSTing a tool to the daemon's approval endpoint (blocking until the daemon
// returns a decision), then emits more output and a result.
const fakeClaude = `#!/bin/sh
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}'
printf '{"tool_name":"Bash","tool_input":{"command":"ls"}}' | curl -s -X POST --data-binary @- "$OCULUS_APPROVE_URL" >/dev/null
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'
echo '{"type":"result","subtype":"success"}'
`

func TestClaudeCodeProvider_E2E(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude.sh")
	if err := os.WriteFile(script, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}

	p := New(script)
	if p.Name() != "claude-code" {
		t.Fatalf("Name = %q", p.Name())
	}

	ctx := context.Background()
	sess, err := p.Create(ctx, dir, "do it")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var gotOutput, gotApproval, gotIdle bool
	timeout := time.After(10 * time.Second)

	for !(gotOutput && gotApproval && gotIdle) {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				gotIdle = true // stream closed = session ended
				continue
			}
			switch ev.Type {
			case protocol.TypeOutputDelta:
				gotOutput = true
			case protocol.TypeApprovalRequest:
				gotApproval = true
				ar := ev.Payload.(protocol.ApprovalRequest)
				if ar.Tool == "" {
					t.Fatalf("approval tool empty: %+v", ar)
				}
				if err := sess.Respond(ctx, ar.ApprovalID, protocol.DecisionAllow); err != nil {
					t.Fatal(err)
				}
			case protocol.TypeSessionStatus:
				ss := ev.Payload.(protocol.SessionStatus)
				if ss.Status == protocol.StatusIdle || ss.Status == protocol.StatusDone {
					gotIdle = true
				}
			}
		case <-timeout:
			t.Fatalf("timeout: output=%v approval=%v idle=%v", gotOutput, gotApproval, gotIdle)
		}
	}

	if !gotApproval {
		t.Fatal("expected an approval request via the hook")
	}
}
