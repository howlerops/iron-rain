package pi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A fake `pi --mode rpc` that speaks the documented JSONL protocol: thinking + text
// deltas, a confirm() approval that BLOCKS until answered, then a tool + more text +
// agent_end. Uses pi's real event shapes (docs/rpc.md), so it validates the parser.
const fakePiRPC = `#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"hmm"}}'
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"working"}}'
echo '{"type":"extension_ui_request","id":"c1","method":"confirm","toolName":"bash","title":"Run bash?","message":"ls"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"extension_ui_response"'*)
      echo '{"type":"tool_execution_start","toolName":"bash","args":{"command":"ls"}}'
      echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"done"}}'
      echo '{"type":"agent_end"}'
      exit 0;;
  esac
done
`

func TestPiProvider_E2E(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi.sh")
	if err := os.WriteFile(script, []byte(fakePiRPC), 0o755); err != nil {
		t.Fatal(err)
	}

	p := New([]string{script})
	if p.Name() != "pi" {
		t.Fatalf("Name = %q", p.Name())
	}

	ctx := context.Background()
	sess, err := p.Create(ctx, dir, "list files")
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
				if ar.Tool != "bash" || ar.Detail != "ls" {
					t.Fatalf("approval = %+v", ar)
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
			t.Fatalf("timeout: thinking=%v output=%v approval=%v idle=%v", gotThinking, gotOutput, gotApproval, gotIdle)
		}
	}
	if !gotThinking {
		t.Fatal("expected a thinking event")
	}
	if !gotApproval {
		t.Fatal("expected a confirm approval")
	}
}

func TestPi_NoCommand(t *testing.T) {
	if _, err := New(nil).Create(context.Background(), "", "hi"); err == nil {
		t.Fatal("expected an error when no command is configured")
	}
}

func TestTodosFromToolArgs(t *testing.T) {
	// A todo-style extension tool call → normalized todos.
	args := map[string]any{"todos": []any{
		map[string]any{"content": "write tests", "status": "in_progress"},
		map[string]any{"content": "ship", "status": "pending"},
	}}
	todos, ok := todosFromToolArgs("todowrite", args)
	if !ok || len(todos) != 2 || todos[0].Content != "write tests" || todos[0].Status != "in_progress" {
		t.Fatalf("todowrite → %+v, ok=%v", todos, ok)
	}
	// A non-todo tool → ignored.
	if _, ok := todosFromToolArgs("bash", map[string]any{"command": "ls"}); ok {
		t.Error("bash should not yield todos")
	}
	// Missing list → ignored.
	if _, ok := todosFromToolArgs("todo", map[string]any{}); ok {
		t.Error("empty args should not yield todos")
	}
}
