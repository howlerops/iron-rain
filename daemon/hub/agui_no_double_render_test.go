package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/agui"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// End-to-end over the REAL pieces: a real AG-UI backend speaking SSE, the real agui adapter, a real
// managedSession, and a real SQLite store. The unit tests in agent/agui prove the adapter no longer
// restates its deltas; this proves the thing the user actually experiences — that a subscriber watching
// live sees the reply once, and that re-opening the session still shows it.
//
// The run deliberately puts a TOOL CALL between two messages, because that is the shape that made the
// bug visible: a tool card SEALS the streaming assistant row, so any finalized message arriving after
// it is APPENDED rather than replacing the row, and the reply renders twice.
func TestAGUIRunRendersEachReplyOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []map[string]any{
			{"type": "RUN_STARTED", "threadId": "t", "runId": "r"},
			{"type": "TEXT_MESSAGE_START", "messageId": "m1", "role": "assistant"},
			{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "Checking the config."},
			{"type": "TEXT_MESSAGE_END", "messageId": "m1"},
			{"type": "TOOL_CALL_START", "toolCallId": "tc1", "toolCallName": "read_file"},
			{"type": "TOOL_CALL_ARGS", "toolCallId": "tc1", "delta": `{"path":"a.go"}`},
			{"type": "TOOL_CALL_END", "toolCallId": "tc1"},
			{"type": "TOOL_CALL_RESULT", "toolCallId": "tc1", "content": "package main"},
			{"type": "TEXT_MESSAGE_START", "messageId": "m2", "role": "assistant"},
			{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m2", "delta": "It is a main package."},
			{"type": "TEXT_MESSAGE_END", "messageId": "m2"},
			{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		} {
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	p := agui.New(agui.Config{Name: "agui", Endpoint: srv.URL})
	sess, err := p.Create(context.Background(), t.TempDir(), "what package is a.go")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = sess.Close() }()

	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, sess, sessionMeta{})
	frames := make(chan []byte, 512)
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()

	go m.run()
	for i := 0; i < 500 && !m.pumpAlive.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.pumpAlive.Load() {
		t.Fatal("the pump never started")
	}

	// Collect what a watching client would actually receive, until the turn goes idle.
	var live strings.Builder
	liveMessages := 0
	deadline := time.After(15 * time.Second)
	for done := false; !done; {
		select {
		case raw := <-frames:
			var f struct {
				Type    string `json:"type"`
				Payload struct {
					SessionID string `json:"session_id"`
					Status    string `json:"status"`
					Role      string `json:"role"`
					Text      string `json:"text"`
				} `json:"payload"`
			}
			if json.Unmarshal(raw, &f) != nil {
				continue
			}
			switch f.Type {
			case protocol.TypeOutputDelta:
				live.WriteString(f.Payload.Text)
			case protocol.TypeSessionMessage:
				if f.Payload.Role == "assistant" {
					liveMessages++
					live.WriteString(f.Payload.Text)
				}
			case protocol.TypeSessionStatus:
				if f.Payload.Status == protocol.StatusIdle {
					// Let anything emitted at turn end land before judging.
					time.Sleep(300 * time.Millisecond)
					done = true
				}
			}
		case <-deadline:
			t.Fatal("the turn never reached idle")
		}
	}

	if n := strings.Count(live.String(), "Checking the config."); n != 1 {
		t.Errorf("the first reply reached the client %d times, want 1 — live text was %q", n, live.String())
	}
	if n := strings.Count(live.String(), "It is a main package."); n != 1 {
		t.Errorf("the second reply reached the client %d times, want 1 — live text was %q", n, live.String())
	}
	if liveMessages != 0 {
		t.Errorf("%d finalized assistant message(s) were delivered live; the turn-end frame must be "+
			"recorded only, or it duplicates the deltas on screen", liveMessages)
	}
	// The two messages are separate paragraphs, not one fused sentence.
	if !strings.Contains(live.String(), "Checking the config.\n\nIt is a main package.") {
		t.Errorf("the two messages were not separated by a blank line: %q", live.String())
	}

	// The tool call completed, and must still read as completed once the turn closes.
	//
	// It did not. This adapter reported a finished card as protocol.StatusDone ("done"), but that is a
	// TURN status — the hub clears a card from turnTools only on "completed" or "error", so every
	// AG-UI tool stayed outstanding for the whole turn and turn close then SEALED it: Status "error",
	// with the seal note written over the tool's real output, broadcast AND persisted. Every
	// successful tool call was rendered and stored as a failure that had eaten its own result.
	var lastTool struct{ status, output string }
	sawTool := false
	for _, raw := range m.fullHistory() {
		var f struct {
			Type    string `json:"type"`
			Payload struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Output string `json:"output"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Type != protocol.TypeSessionTool || f.Payload.ID != "tc1" {
			continue
		}
		sawTool = true
		lastTool.status, lastTool.output = f.Payload.Status, f.Payload.Output
	}
	if !sawTool {
		t.Fatal("the tool call never reached the transcript")
	}
	if lastTool.status != "completed" {
		t.Errorf("tool tc1 ended as %q, want \"completed\" — the hub sealed a card it never saw finish", lastTool.status)
	}
	if lastTool.output != "package main" {
		t.Errorf("tool tc1 output = %q, want its real result", lastTool.output)
	}

	// And the turn has to survive a restart: exactly one finalized assistant message in the history,
	// carrying the whole turn. More than one means the transcript replays the reply twice.
	var finalized []string
	for _, raw := range m.fullHistory() {
		var f struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				Role      string `json:"role"`
				Text      string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		if f.Type == protocol.TypeSessionMessage && f.Payload.Role == "assistant" &&
			f.Payload.SessionID == sess.ID() {
			finalized = append(finalized, f.Payload.Text)
		}
	}
	if len(finalized) != 1 {
		t.Fatalf("history holds %d finalized assistant messages, want exactly 1: %q", len(finalized), finalized)
	}
	if !strings.Contains(finalized[0], "Checking the config.") ||
		!strings.Contains(finalized[0], "It is a main package.") {
		t.Errorf("the persisted turn is missing text: %q", finalized[0])
	}
}
