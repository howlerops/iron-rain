package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// sse writes AG-UI events in the protocol's framing: `data: <single-line JSON>\n\n`.
func sse(w http.ResponseWriter, events ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, e := range events {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// collect drains events until the session reports a terminal status, or the deadline passes.
func collect(t *testing.T, s agent.Session, until func(agent.Event) bool) []agent.Event {
	t.Helper()
	var got []agent.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return got
			}
			got = append(got, ev)
			if until(ev) {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out; collected %d events: %+v", len(got), got)
			return got
		}
	}
}

func idleReached(ev agent.Event) bool {
	st, ok := ev.Payload.(protocol.SessionStatus)
	return ok && st.Status == protocol.StatusIdle
}

func newTestSession(t *testing.T, h http.HandlerFunc) (agent.Session, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p := New(Config{Name: "agui", Endpoint: srv.URL})
	s, err := p.Create(context.Background(), t.TempDir(), "do the thing")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, srv
}

// The reply is streamed ONCE, as deltas.
//
// This adapter also emitted a finalized session.message holding the concatenation of those same
// deltas — its buffer had no other writer, so the frame could not contain anything the client had
// not already been sent. The client only REPLACES a finalized message onto an assistant row that is
// still streaming; a tool card or a generative-UI block seals that row, and the restatement was then
// appended instead. The reply rendered twice, adjacent and identical.
//
// Durability does not depend on it: the hub concatenates the deltas itself and persists them at turn
// end for any provider that never finalizes a message (finalizeTurnTranscript), ringing the frame
// without delivering it — which is precisely the guard this adapter lacked.
func TestStreamsTextOnceAsDeltas(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			map[string]any{"type": "RUN_STARTED", "threadId": "t", "runId": "r"},
			map[string]any{"type": "TEXT_MESSAGE_START", "messageId": "m1", "role": "assistant"},
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "Hello "},
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "world"},
			map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m1"},
			map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		)
	})

	var deltas []string
	for _, ev := range collect(t, s, idleReached) {
		switch p := ev.Payload.(type) {
		case protocol.OutputDelta:
			deltas = append(deltas, p.Text)
		case protocol.SessionMessage:
			if p.Role == "assistant" {
				t.Fatalf("the adapter restated the streamed text as a finalized message: %+v", p)
			}
		}
	}
	if strings.Join(deltas, "") != "Hello world" {
		t.Errorf("deltas = %q, want the streamed text exactly once", deltas)
	}
}

// TEXT_MESSAGE_END is a MESSAGE BOUNDARY, and dropping the restatement must not drop it.
//
// The client folds every delta of a turn into one bubble, so two messages in a single run arrive as
// one run of text: without a separator the last word of the first fuses to the first word of the
// second ("...done.Next I will..."). opencode, claude-code and pi each had to fix this same defect.
// A blank line, not a single newline, because markdown folds a lone newline back into one paragraph.
func TestConsecutiveMessagesAreSeparated(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			map[string]any{"type": "RUN_STARTED", "threadId": "t", "runId": "r"},
			map[string]any{"type": "TEXT_MESSAGE_START", "messageId": "m1", "role": "assistant"},
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "First."},
			map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m1"},
			map[string]any{"type": "TEXT_MESSAGE_START", "messageId": "m2", "role": "assistant"},
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m2", "delta": "Second."},
			map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m2"},
			map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		)
	})

	var text strings.Builder
	for _, ev := range collect(t, s, idleReached) {
		if p, ok := ev.Payload.(protocol.OutputDelta); ok {
			text.WriteString(p.Text)
		}
	}
	if got := text.String(); got != "First.\n\nSecond." {
		t.Errorf("assembled text = %q, want the two messages separated by a blank line", got)
	}
}

// A trailing boundary must not leave a dangling separator: the break is only spent when more text
// actually follows, so a run whose last event is TEXT_MESSAGE_END ends with the text, not whitespace.
func TestTrailingBoundaryEmitsNothing(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "Only."},
			map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m1"},
			map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		)
	})
	var text strings.Builder
	for _, ev := range collect(t, s, idleReached) {
		if p, ok := ev.Payload.(protocol.OutputDelta); ok {
			text.WriteString(p.Text)
		}
	}
	if got := text.String(); got != "Only." {
		t.Errorf("assembled text = %q, want no trailing separator", got)
	}
}

// The turn must still close when a backend never sends TEXT_MESSAGE_END — otherwise the turn engine
// waits on a completion that isn't coming. The text still has to arrive, and still only once: this
// truncated path is where the removed flush was load-bearing in appearance only.
func TestRunEndClosesAnUnterminatedMessage(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "partial"},
			map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		)
	})
	var text strings.Builder
	sawIdle := false
	for _, ev := range collect(t, s, idleReached) {
		switch p := ev.Payload.(type) {
		case protocol.OutputDelta:
			text.WriteString(p.Text)
		case protocol.SessionMessage:
			if p.Role == "assistant" {
				t.Fatalf("the truncated stream was restated as a finalized message: %+v", p)
			}
		case protocol.SessionStatus:
			if p.Status == protocol.StatusIdle {
				sawIdle = true
			}
		}
	}
	if text.String() != "partial" {
		t.Errorf("assembled text = %q, want the partial message", text.String())
	}
	if !sawIdle {
		t.Error("the turn never reached idle, so the turn engine would wait forever")
	}
}

// AG-UI streams a tool call across four event types; our protocol carries ONE frame updated in place.
func TestToolCallFoldsIntoOneCard(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			map[string]any{"type": "TOOL_CALL_START", "toolCallId": "tc1", "toolCallName": "bash"},
			map[string]any{"type": "TOOL_CALL_ARGS", "toolCallId": "tc1", "delta": `{"comm`},
			map[string]any{"type": "TOOL_CALL_ARGS", "toolCallId": "tc1", "delta": `and":"npm test"}`},
			map[string]any{"type": "TOOL_CALL_END", "toolCallId": "tc1"},
			map[string]any{"type": "TOOL_CALL_RESULT", "messageId": "m1", "toolCallId": "tc1", "content": "3 passing"},
			map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		)
	})

	var tools []protocol.SessionTool
	for _, ev := range collect(t, s, idleReached) {
		if p, ok := ev.Payload.(protocol.SessionTool); ok {
			tools = append(tools, p)
		}
	}
	if len(tools) == 0 {
		t.Fatal("no tool frames emitted")
	}
	for _, tl := range tools {
		if tl.ID != "tc1" || tl.Name != "bash" {
			t.Fatalf("every frame must describe the same call: %+v", tl)
		}
	}
	last := tools[len(tools)-1]
	// "completed", not protocol.StatusDone: a finished tool CARD is "completed" across every adapter,
	// and the hub retires it from the turn's outstanding set on that exact word. Reporting the turn
	// status "done" here left the card outstanding until turn close, which then sealed it as an error
	// over its own output.
	if last.Status != "completed" || last.Output != "3 passing" {
		t.Errorf("final frame = %+v, want a completed card carrying the result", last)
	}
	// The title comes from args that were only valid JSON once fully assembled — a mid-stream
	// fragment must degrade to the bare tool name rather than corrupt the card.
	if last.Title != "bash · npm test" {
		t.Errorf("title = %q, want the reassembled command", last.Title)
	}
}

// An interrupt is AG-UI's approval request. It TERMINATES the run, and the answer must ride the next
// run's `resume` list — there is no in-place resolve call.
func TestInterruptBecomesApprovalAndResumes(t *testing.T) {
	var runs int
	resumed := make(chan []resumeEntry, 1)
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in runInput
		_ = json.Unmarshal(body, &in)
		runs++
		if runs == 1 {
			sse(w, map[string]any{
				"type": "RUN_FINISHED", "threadId": in.ThreadID, "runId": in.RunID,
				"outcome": map[string]any{"type": "interrupt", "interrupts": []map[string]any{
					{"id": "int-1", "reason": "tool_call", "message": "Run `rm -rf build`?"},
				}},
			})
			return
		}
		resumed <- in.Resume
		sse(w,
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m2", "delta": "done"},
			map[string]any{"type": "RUN_FINISHED", "threadId": in.ThreadID, "runId": in.RunID, "outcome": map[string]any{"type": "success"}},
		)
	})

	var req protocol.ApprovalRequest
	collect(t, s, func(ev agent.Event) bool {
		if p, ok := ev.Payload.(protocol.ApprovalRequest); ok {
			req = p
			return true
		}
		return false
	})
	if req.ApprovalID != "int-1" || req.Detail != "Run `rm -rf build`?" {
		t.Fatalf("interrupt did not become an approval request: %+v", req)
	}

	if err := s.Respond(context.Background(), "int-1", protocol.DecisionAllow); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	select {
	case got := <-resumed:
		if len(got) != 1 || got[0].InterruptID != "int-1" || got[0].Status != "resolved" {
			t.Fatalf("the follow-up run carried %+v, want a resolved resume for int-1", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached a follow-up run")
	}
}

// Every run must reuse the session's thread id, or the backend loses conversation continuity.
func TestThreadIDIsStableAcrossRuns(t *testing.T) {
	seen := make(chan string, 4)
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in runInput
		_ = json.Unmarshal(body, &in)
		seen <- in.ThreadID
		sse(w, map[string]any{"type": "RUN_FINISHED", "threadId": in.ThreadID, "runId": in.RunID,
			"outcome": map[string]any{"type": "success"}})
	})
	collect(t, s, idleReached)
	first := <-seen

	if err := s.Prompt(context.Background(), "again"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	select {
	case second := <-seen:
		if second != first || first == "" {
			t.Fatalf("thread id changed between runs: %q then %q", first, second)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second run never started")
	}
}

// Probe is what keeps this provider legible to the turn engine, which AG-UI itself gives no way to
// answer. It must report busy while the run's request is open and not busy once it closes.
func TestProbeTracksRunLifetime(t *testing.T) {
	release := make(chan struct{})
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		sse(w, map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r",
			"outcome": map[string]any{"type": "success"}})
	})

	pr, ok := s.(interface {
		Probe(context.Context) (bool, error)
	})
	if !ok {
		t.Fatal("session must implement Prober — without it the turn engine cannot see this provider")
	}

	// Wait for the run to actually be in flight rather than asserting on a race.
	var busy bool
	for i := 0; i < 100; i++ {
		if busy, _ = pr.Probe(context.Background()); busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !busy {
		t.Fatal("Probe reported idle while a run was open")
	}

	close(release)
	collect(t, s, idleReached)
	if busy, _ = pr.Probe(context.Background()); busy {
		t.Error("Probe still reports busy after the run finished")
	}
}

// A non-200 must surface as an error status rather than a silently dead session.
func TestHTTPErrorSurfaces(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	})
	var status protocol.SessionStatus
	collect(t, s, func(ev agent.Event) bool {
		p, ok := ev.Payload.(protocol.SessionStatus)
		if ok && p.Status == protocol.StatusError {
			status = p
			return true
		}
		return false
	})
	if !strings.Contains(status.Detail, "502") {
		t.Errorf("error detail should name the status code, got %q", status.Detail)
	}
}

// A malformed frame must be skipped, not abort the run — the stream is a shared channel and one bad
// event should not cost the user the rest of the turn.
func TestMalformedFrameIsSkipped(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {not json\n\n")
		b, _ := json.Marshal(map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m", "delta": "survived"})
		fmt.Fprintf(w, "data: %s\n\n", b)
		b2, _ := json.Marshal(map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r",
			"outcome": map[string]any{"type": "success"}})
		fmt.Fprintf(w, "data: %s\n\n", b2)
	})
	var text string
	for _, ev := range collect(t, s, idleReached) {
		if p, ok := ev.Payload.(protocol.OutputDelta); ok {
			text += p.Text
		}
	}
	if text != "survived" {
		t.Errorf("text after a malformed frame = %q, want the following event to still arrive", text)
	}
}

// The message boundary is per-TURN state. A turn that ends on TEXT_MESSAGE_END owes a separator that
// is never spent; the client renders each turn as its own row, so carrying that debt into the next
// turn opened the following reply with a blank line.
func TestBoundaryDoesNotLeakAcrossTurns(t *testing.T) {
	s, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "Turn one."},
			map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m1"},
			map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r", "outcome": map[string]any{"type": "success"}},
		)
	})
	collect(t, s, idleReached) // first turn, ends owing a break

	if err := s.Prompt(context.Background(), "again"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var text strings.Builder
	for _, ev := range collect(t, s, idleReached) {
		if p, ok := ev.Payload.(protocol.OutputDelta); ok {
			text.WriteString(p.Text)
		}
	}
	if got := text.String(); strings.HasPrefix(got, "\n") {
		t.Errorf("second turn opened with the previous turn's separator: %q", got)
	}
}

// Prompt must never report success for a message it did not send.
//
// It used to sample `running`, drop the lock, return nil, and leave runTurn to re-check — so when
// several callers sampled an IDLE session at once they were all told the prompt was accepted, and
// then all but one hit runTurn's re-check and returned silently. Those messages were gone:
// acknowledged by the API, never sent to the backend, and nothing anywhere reported it. Either the
// run starts or Prompt returns an error.
func TestPromptNeverSilentlyDropsAMessage(t *testing.T) {
	var mu sync.Mutex
	var sent []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		for _, m := range in.Messages {
			sent = append(sent, m.Content)
		}
		mu.Unlock()
		sse(w, map[string]any{"type": "RUN_FINISHED", "threadId": "t", "runId": "r",
			"outcome": map[string]any{"type": "success"}})
	}))
	defer srv.Close()

	prov := New(Config{Name: "agui", Endpoint: srv.URL})
	// No creating prompt: the session starts IDLE, which is what opens the window.
	sess, err := prov.Create(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = sess.Close() }()

	const callers = 16
	var wg sync.WaitGroup
	var accMu sync.Mutex
	accepted := make([]string, 0, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := fmt.Sprintf("follow-up-%d", i)
			<-start // release them together, so they sample `running` at the same instant
			if err := sess.Prompt(context.Background(), text); err == nil {
				accMu.Lock()
				accepted = append(accepted, text)
				accMu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	accMu.Lock()
	want := append([]string(nil), accepted...)
	accMu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n >= len(want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, w := range want {
		found := false
		for _, got := range sent {
			if got == w {
				found = true
			}
		}
		if !found {
			t.Errorf("Prompt accepted %q but it never reached the backend (accepted %d, delivered %d): %q",
				w, len(want), len(sent), sent)
		}
	}
}
