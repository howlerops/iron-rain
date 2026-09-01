package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// healthyStub is a turn that goes exactly as it should: the SSE streams the reply as deltas AND
// delivers session.idle, and only then does the message POST return. Nothing was missed, so there is
// nothing to recover.
type healthyStub struct {
	eventConns int32
	getMsgs    int32 // how many times resyncLast fetched the message list
}

func (h *healthyStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_x", "title": "stub"})

	case r.Method == http.MethodGet && r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		if atomic.AddInt32(&h.eventConns, 1) == 1 {
			for _, frame := range []string{
				`{"type":"message.part.delta","properties":{"sessionID":"ses_x","field":"text","delta":"# Repository Contents\n"}}`,
				`{"type":"message.part.delta","properties":{"sessionID":"ses_x","field":"text","delta":"- NOTES.md - placeholder"}}`,
				`{"type":"session.idle","properties":{"sessionID":"ses_x"}}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", frame)
				if fl != nil {
					fl.Flush()
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		<-r.Context().Done()

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		time.Sleep(120 * time.Millisecond) // returns AFTER the SSE already said idle
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
		atomic.AddInt32(&h.getMsgs, 1)
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"info": map[string]any{"role": "user"}, "parts": []map[string]any{{"type": "text", "text": "go"}}},
			{"info": map[string]any{"role": "assistant", "id": "msg_1"},
				"parts": []map[string]any{{"type": "text", "text": "# Repository Contents\n- NOTES.md - placeholder"}}},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// A healthy turn must not re-send text the stream already delivered.
//
// resyncLast was called on EVERY clean POST return, not just when the stream had missed the end. Its
// frame is a finalized assistant message carrying the last message's full text, and it loses the race
// to the session.idle the SSE already sent — so the client, which replaces only a still-STREAMING
// row, had already sealed that row and APPENDED the text instead. The reply rendered twice, adjacent
// and identical.
//
// Nothing was duplicated on disk or in the replay ring (the frame carries a msg id and is stored
// once), which is why a hunt for duplicated frames came back clean and this went unexplained.
func TestOpenCode_HealthyTurnDoesNotResync(t *testing.T) {
	h := &healthyStub{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	sess, err := New(srv.URL).Create(context.Background(), "/repo", "go")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var deltas int
	var finalized []string
	deadline := time.After(8 * time.Second)
	idles := 0
	for idles < 2 { // the SSE idle, then the POST-return backstop
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				goto done
			}
			switch p := ev.Payload.(type) {
			case protocol.OutputDelta:
				deltas++
			case protocol.SessionMessage:
				if p.Role == "assistant" {
					finalized = append(finalized, p.Text)
				}
			case protocol.SessionStatus:
				if p.Status == protocol.StatusIdle {
					idles++
				}
			}
		case <-deadline:
			goto done
		}
	}
done:
	if deltas == 0 {
		t.Fatal("the stub's streamed reply never arrived — the test proves nothing")
	}
	if len(finalized) != 0 {
		t.Fatalf("a healthy turn re-sent its reply as a finalized message (%d): %q — the client appends this as a second copy",
			len(finalized), finalized)
	}
	if n := atomic.LoadInt32(&h.getMsgs); n != 0 {
		t.Fatalf("resyncLast ran %d time(s) on a turn where the stream delivered everything", n)
	}
}
