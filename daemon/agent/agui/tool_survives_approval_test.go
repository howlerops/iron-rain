package agui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// The user approves a command, it runs, and its output never appears.
//
// TOOL_CALL_RESULT is optional in AG-UI, so the adapter closes out any card still open when a run
// ends — otherwise the hub seals it as an error at turn close and paints every tool red. But an
// APPROVAL interrupt also ends the run, with the tool still genuinely open: the human has not
// answered yet, and the resume run is what delivers the result. Sweeping there retired the card and
// forgot its call id, so when the approved command finished, `emitTool` looked the id up, found
// nothing, and returned silently.
//
// The existing interrupt test emits RUN_FINISHED with no preceding TOOL_CALL_START — it has no open
// card to sweep, so it passed throughout. This one has the card, which is what every real
// human-in-the-loop run looks like: you are asked to approve a tool call, so a tool call is open.
func TestAnApprovedToolStillReportsItsOutput(t *testing.T) {
	var runs int
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in runInput
		_ = json.Unmarshal(body, &in)
		runs++
		if runs == 1 {
			// A tool call opens, then the run stops to ask. This is the shape of the flow.
			sse(w,
				map[string]any{"type": "TOOL_CALL_START", "toolCallId": "tc1", "toolCallName": "bash"},
				map[string]any{"type": "TOOL_CALL_ARGS", "toolCallId": "tc1", "delta": `{"command":"npm test"}`},
				map[string]any{"type": "TOOL_CALL_END", "toolCallId": "tc1"},
				map[string]any{"type": "RUN_FINISHED", "threadId": in.ThreadID, "runId": in.RunID,
					"outcome": map[string]any{"type": "interrupt", "interrupts": []map[string]any{
						{"id": "int-1", "reason": "tool_call", "message": "Run `npm test`?"},
					}}},
			)
			return
		}
		// Approved: the resume run carries the tool's result.
		sse(w,
			map[string]any{"type": "TOOL_CALL_RESULT", "toolCallId": "tc1", "content": "3 tests passed"},
			map[string]any{"type": "RUN_FINISHED", "threadId": in.ThreadID, "runId": in.RunID,
				"outcome": map[string]any{"type": "success"}},
		)
	})

	// Run one: collect up to the approval request, and check the card was NOT retired behind it.
	var sealed *protocol.SessionTool
	evs := collect(t, s, func(ev agent.Event) bool {
		if tl, ok := ev.Payload.(protocol.SessionTool); ok && protocol.IsToolFinished(tl.Status) {
			c := tl
			sealed = &c
		}
		_, isApproval := ev.Payload.(protocol.ApprovalRequest)
		return isApproval
	})
	if sealed != nil {
		t.Errorf("the tool card was closed out as %q with output %q while the user had not answered "+
			"yet — the adapter has now forgotten the call id that the resume run will report against",
			sealed.Status, sealed.Output)
	}
	_ = evs

	if err := s.Respond(context.Background(), "int-1", protocol.DecisionAllow); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	// Run two: the result must actually surface.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("the stream ended without the approved tool ever reporting")
			}
			if tl, ok := ev.Payload.(protocol.SessionTool); ok && tl.ID == "tc1" {
				if tl.Status != protocol.ToolCompleted {
					continue
				}
				if tl.Output != "3 tests passed" {
					t.Fatalf("tool output = %q, want the approved command's result", tl.Output)
				}
				return
			}
		case <-deadline:
			t.Fatal("the approved command ran and its output never reached the user: the card was " +
				"swept when the run paused to ask, so its result arrived for a call the adapter no " +
				"longer knew about and was dropped")
		}
	}
}
