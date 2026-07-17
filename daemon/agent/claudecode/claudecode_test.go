package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A fake sidecar that speaks the stdio protocol: it emits thinking + text + an
// approval, BLOCKS reading stdin until the daemon answers the approval, then emits a
// tool step + more text + idle. Proves the persistent streaming + blocking-approval
// (canUseTool) contract without needing Node/the SDK/an LLM.
const fakeSidecar = `#!/bin/sh
echo '{"t":"session","id":"'"$OCULUS_SESSION_ID"'"}'
echo '{"t":"thinking","text":"let me think"}'
echo '{"t":"text","text":"working"}'
echo '{"t":"approval","id":"ap1","tool":"Bash","detail":"ls"}'
while IFS= read -r line; do
  case "$line" in
    *'"t":"approval"'*)
      echo '{"t":"tool","tool":"Bash","detail":"ls"}'
      echo '{"t":"text","text":"done"}'
      echo '{"t":"idle"}'
      exit 0;;
  esac
done
`

func TestClaudeCodeSidecar_E2E(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-sidecar.sh")
	if err := os.WriteFile(script, []byte(fakeSidecar), 0o755); err != nil {
		t.Fatal(err)
	}

	p := New([]string{script})
	if p.Name() != "claude-code" {
		t.Fatalf("Name = %q", p.Name())
	}

	ctx := context.Background()
	sess, err := p.Create(ctx, dir, "do it")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var gotThinking, gotOutput, gotApproval, gotIdle bool
	timeout := time.After(10 * time.Second)

	for !(gotOutput && gotApproval && gotIdle) {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				gotIdle = true
				continue
			}
			switch ev.Type {
			case protocol.TypeThinking:
				gotThinking = true
			case protocol.TypeOutputDelta:
				gotOutput = true
			case protocol.TypeApprovalRequest:
				gotApproval = true
				ar := ev.Payload.(protocol.ApprovalRequest)
				if ar.Tool != "Bash" || ar.Detail != "ls" {
					t.Fatalf("approval = %+v", ar)
				}
				// Answering must unblock the sidecar (the streaming canUseTool contract).
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
			t.Fatalf("timeout: thinking=%v output=%v approval=%v idle=%v", gotThinking, gotOutput, gotApproval, gotIdle)
		}
	}

	if !gotThinking {
		t.Fatal("expected a thinking event from the sidecar")
	}
	if !gotApproval {
		t.Fatal("expected an approval request")
	}
}

func TestClaudeCode_NoSidecar(t *testing.T) {
	if _, err := New(nil).Create(context.Background(), "", "hi"); err == nil {
		t.Fatal("expected an error when no sidecar is configured")
	}
}
