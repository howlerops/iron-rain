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

// dropStub reproduces the "stuck opencode turn" failure: the /event SSE stream sends one delta then
// DROPS mid-turn WITHOUT ever sending session.idle (a blip / opencode idle timeout / long turn), while
// the agent keeps running and the message POST eventually returns. GET /session/{id}/message serves
// the final assistant text so the adapter's resync can recover it.
type dropStub struct {
	eventConns int32 // number of /event subscriptions seen (first one drops; later ones just block)
	msgPosted  chan struct{}
}

func (d *dropStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		if atomic.AddInt32(&d.eventConns, 1) == 1 {
			// First subscription: stream one partial delta, then DROP the stream (return) with no idle.
			fmt.Fprintf(w, "data: %s\n\n", `{"type":"message.part.delta","properties":{"sessionID":"ses_x","field":"text","delta":"Partial"}}`)
			if fl != nil {
				fl.Flush()
			}
			return // connection closes → the agent "keeps working" but we've lost the event stream
		}
		<-r.Context().Done() // reconnect attempts just hang (still no events)

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		// The turn runs a moment server-side, then the POST returns (turn complete) — the reliable
		// completion signal the SSE failed to deliver.
		select {
		case d.msgPosted <- struct{}{}:
		default:
		}
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
		// resyncLast: the session's final messages, including the full assistant text.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"info": map[string]any{"role": "user"}, "parts": []map[string]any{{"type": "text", "text": "go"}}},
			{"info": map[string]any{"role": "assistant"}, "parts": []map[string]any{{"type": "text", "text": "Partial and the rest"}}},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestOpenCode_RecoversFromDroppedStream is the regression for the recurring "opencode stuck" bug: the
// SSE stream drops mid-turn (no session.idle), yet the app must still (a) receive session.idle so it
// unsticks, and (b) recover the turn's full output via resync — driven off the message POST returning.
func TestOpenCode_RecoversFromDroppedStream(t *testing.T) {
	d := &dropStub{msgPosted: make(chan struct{}, 1)}
	srv := httptest.NewServer(d)
	defer srv.Close()

	ctx := context.Background()
	sess, err := New(srv.URL).Create(ctx, "/repo", "go")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var gotPartial, gotFullText, gotIdle bool
	timeout := time.After(6 * time.Second)
	for !(gotIdle && gotFullText) {
		select {
		case ev := <-sess.Events():
			switch ev.Type {
			case protocol.TypeOutputDelta:
				if ev.Payload.(protocol.OutputDelta).Text == "Partial" {
					gotPartial = true
				}
			case protocol.TypeSessionMessage:
				m := ev.Payload.(protocol.SessionMessage)
				if m.Role == "assistant" && m.Text == "Partial and the rest" {
					gotFullText = true // resync recovered the completed turn's text
				}
			case protocol.TypeSessionStatus:
				if s := ev.Payload.(protocol.SessionStatus); s.Status == protocol.StatusIdle {
					gotIdle = true // POST-return backstop unstuck the session despite no SSE idle
				}
			}
		case <-timeout:
			t.Fatalf("stuck: partial=%v fullText=%v idle=%v", gotPartial, gotFullText, gotIdle)
		}
	}
	if !gotPartial {
		t.Fatal("expected the pre-drop partial delta")
	}
}
