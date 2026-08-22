package opencode_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestSessionErrorSurfacesTheProviderMessage: a provider failure must reach the user.
//
// opencode answers the message POST with HTTP 200 and reports the real outcome as a `session.error`
// event, then follows it with `session.idle`. We handled created/updated/idle but NOT error, so the
// turn closed cleanly having streamed nothing and explained nothing — a model outage was
// indistinguishable from the agent choosing not to reply, and the user just watched their prompt
// vanish.
//
// Found live: opencode.ai/zen returned "Model is unavailable" for one repo while another worked, so
// this presented as "opencode is broken in this project" with no way to tell why.
func TestSessionErrorSurfacesTheProviderMessage(t *testing.T) {
	const wantMsg = "Error from provider (Console): Upstream request failed: Model is unavailable."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flush := func() {
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			// The exact shape observed against a real server.
			fmt.Fprintf(w, "data: {\"type\":\"session.error\",\"properties\":{\"sessionID\":\"s1\",\"error\":{\"name\":\"APIError\",\"data\":{\"message\":%q,\"statusCode\":400}}}}\n\n", wantMsg)
			flush()
			fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"s1\"}}\n\n")
			flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"s1","directory":"/tmp"}`))
		default:
			// The message POST succeeds — that is the whole point: HTTP says fine, the event says no.
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream ended without surfacing the provider error")
			}
			st, isStatus := ev.Payload.(protocol.SessionStatus)
			if !isStatus {
				continue
			}
			if st.Status == protocol.StatusError {
				if !strings.Contains(st.Detail, wantMsg) {
					t.Fatalf("detail = %q, want the provider's own words — a generic message leaves the user unable to tell an outage from a bad prompt", st.Detail)
				}
				return
			}
			if st.Status == protocol.StatusIdle {
				t.Fatal("turn closed idle after a provider error — this is the silent-failure bug")
			}
		case <-deadline:
			t.Fatal("no error status emitted")
		}
	}
}
