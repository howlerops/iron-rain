package agui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// officialSDKFrames is a VERBATIM capture of what the official AG-UI Go SDK's SSEWriter puts on the
// wire, taken by running a server built against github.com/ag-ui-protocol/ag-ui/sdks/community/go
// and recording the response.
//
// Hand-written fixtures are what let the pi adapter ship broken: a mock only sends the shapes its
// author already knew about, and pi's real output contained one nobody had anticipated. So this is
// their bytes, not ours. Note the `id:` lines — the Go SDK emits them, our own httptest mock did
// not, and a decoder that failed to skip them would drop every event.
const officialSDKFrames = `id: RUN_STARTED_1787351262250
data: {"type":"RUN_STARTED","timestamp":1787351262250,"threadId":"t1","runId":"r1"}
id: TEXT_MESSAGE_START_1787351262250
data: {"type":"TEXT_MESSAGE_START","timestamp":1787351262250,"messageId":"m1","role":"assistant"}
id: TEXT_MESSAGE_CONTENT_1787351262250
data: {"type":"TEXT_MESSAGE_CONTENT","timestamp":1787351262250,"messageId":"m1","delta":"Hello "}
id: TEXT_MESSAGE_CONTENT_1787351262250
data: {"type":"TEXT_MESSAGE_CONTENT","timestamp":1787351262250,"messageId":"m1","delta":"world"}
id: TEXT_MESSAGE_END_1787351262250
data: {"type":"TEXT_MESSAGE_END","timestamp":1787351262250,"messageId":"m1"}
id: TOOL_CALL_START_1787351262250
data: {"type":"TOOL_CALL_START","timestamp":1787351262250,"toolCallId":"tc1","toolCallName":"bash"}
id: TOOL_CALL_ARGS_1787351262250
data: {"type":"TOOL_CALL_ARGS","timestamp":1787351262250,"toolCallId":"tc1","delta":"{\"command\":\"npm test\"}"}
id: TOOL_CALL_END_1787351262250
data: {"type":"TOOL_CALL_END","timestamp":1787351262250,"toolCallId":"tc1"}
id: TOOL_CALL_RESULT_1787351262250
data: {"type":"TOOL_CALL_RESULT","timestamp":1787351262250,"messageId":"m2","toolCallId":"tc1","content":"3 passing","role":"tool"}
id: RUN_FINISHED_1787351262250
data: {"type":"RUN_FINISHED","timestamp":1787351262250,"threadId":"t1","runId":"r1","outcome":{"type":"success"}}`

func TestDecodesOfficialSDKWireFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, line := range strings.Split(officialSDKFrames, "\n") {
			fmt.Fprintf(w, "%s\n", line)
			if line == "" {
				continue
			}
		}
		fmt.Fprint(w, "\n")
	}))
	defer srv.Close()

	p := New(Config{Name: "agui", Endpoint: srv.URL})
	s, err := p.Create(context.Background(), t.TempDir(), "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer s.Close()

	var text strings.Builder
	var tools []protocol.SessionTool
	for _, ev := range drain(t, s) {
		switch pl := ev.Payload.(type) {
		case protocol.OutputDelta:
			text.WriteString(pl.Text)
		case protocol.SessionTool:
			tools = append(tools, pl)
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q, want %q", text.String(), "Hello world")
	}
	if len(tools) == 0 {
		t.Fatal("no tool frames decoded from the official format")
	}
	last := tools[len(tools)-1]
	if last.ID != "tc1" || last.Name != "bash" || last.Status != "completed" || last.Output != "3 passing" {
		t.Errorf("final tool card = %+v", last)
	}
	// The title comes from argument fragments that are only valid JSON once reassembled.
	if last.Title != "bash · npm test" {
		t.Errorf("title = %q", last.Title)
	}
}

// drain collects events until the session reports a terminal status or the stream ends.
func drain(t *testing.T, s agent.Session) []agent.Event {
	t.Helper()
	var out []agent.Event
	for ev := range s.Events() {
		out = append(out, ev)
		if st, ok := ev.Payload.(protocol.SessionStatus); ok {
			if st.Status == protocol.StatusIdle || st.Status == protocol.StatusError {
				return out
			}
		}
	}
	return out
}
