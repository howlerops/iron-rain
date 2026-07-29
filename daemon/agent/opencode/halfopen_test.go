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

// halfOpenStub reproduces the real "opencode gets stuck on long tasks and never catches up" failure:
// the /event SSE connection goes HALF-OPEN — the first subscription streams a delta and then just
// HANGS (no more bytes, no FIN/RST), exactly like a laptop sleeping or a NAT dropping the flow. The
// message POST never returns (the turn is "still running server-side"), so the POST-return backstop
// can't help. Recovery MUST come from the SSE idle read-deadline: the stalled read times out, the
// stream reconnects, and the SECOND subscription delivers the live session.idle the app was waiting on.
type halfOpenStub struct {
	eventConns int32
	done       chan struct{} // closed by the test to release hanging handlers so srv.Close() can finish
}

func (h *halfOpenStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
			// First subscription: one delta, then go SILENT (hold the socket open, send nothing). This
			// is the half-open hang the idle read-deadline exists to break.
			fmt.Fprintf(w, "data: %s\n\n", `{"type":"message.part.delta","properties":{"sessionID":"ses_x","field":"text","delta":"Partial"}}`)
			if fl != nil {
				fl.Flush()
			}
			select { // hang until the client force-closes on idle timeout (or the test tears down)
			case <-r.Context().Done():
			case <-h.done:
			}
			return
		}
		// Second subscription (after the idle-timeout reconnect): deliver the live session.idle the
		// first connection never sent, proving live events resume on the fresh stream.
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"session.idle","properties":{"sessionID":"ses_x"}}`)
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-h.done:
		}

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		select { // the turn never returns during the test → turnActive stays true
		case <-r.Context().Done():
		case <-h.done:
		}

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
		// resyncLast (fired on reconnect because a turn is in flight): the completed assistant text.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"info": map[string]any{"role": "assistant"}, "parts": []map[string]any{{"type": "text", "text": "Partial and the rest"}}},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestOpenCode_RecoversFromHalfOpenStall is the regression for the half-open SSE hang: with no idle
// read-deadline the scanner blocks forever and the session never recovers. With it, the stream
// reconnects and both the live session.idle (from the second connection) and the resynced final text
// arrive — WITHOUT the message POST ever returning.
func TestOpenCode_RecoversFromHalfOpenStall(t *testing.T) {
	h := &halfOpenStub{done: make(chan struct{})}
	srv := httptest.NewServer(h)
	// Teardown order (defers run LIFO): release hanging handlers → close the session → close the server.
	defer srv.Close()
	ctx := context.Background()
	// A short idle timeout so the half-open hang is detected in the test instead of the 120s prod window.
	sess, err := newProvider(srv.URL, 300*time.Millisecond).Create(ctx, "/repo", "go")
	if err != nil {
		close(h.done)
		t.Fatal(err)
	}
	defer sess.Close()
	defer close(h.done)

	var gotPartial, gotIdle, gotFullText bool
	timeout := time.After(6 * time.Second)
	for !(gotIdle && gotFullText) {
		select {
		case ev := <-sess.Events():
			switch ev.Type {
			case protocol.TypeOutputDelta:
				if ev.Payload.(protocol.OutputDelta).Text == "Partial" {
					gotPartial = true
				}
			case protocol.TypeSessionStatus:
				if s := ev.Payload.(protocol.SessionStatus); s.Status == protocol.StatusIdle {
					gotIdle = true // live idle over the RECONNECTED stream (POST never returned)
				}
			case protocol.TypeSessionMessage:
				m := ev.Payload.(protocol.SessionMessage)
				if m.Role == "assistant" && m.Text == "Partial and the rest" {
					gotFullText = true // resync-on-reconnect recovered the completed text
				}
			}
		case <-timeout:
			t.Fatalf("stuck on half-open stream: partial=%v idle=%v fullText=%v conns=%d",
				gotPartial, gotIdle, gotFullText, atomic.LoadInt32(&h.eventConns))
		}
	}
	if !gotPartial {
		t.Fatal("expected the pre-stall partial delta")
	}
	if n := atomic.LoadInt32(&h.eventConns); n < 2 {
		t.Fatalf("expected a reconnect (>=2 /event subscriptions), got %d", n)
	}
}
