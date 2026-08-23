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

// opencode has TWO ways of reporting a failed turn, and we only handled one.
//
// TestSessionErrorSurfacesTheProviderMessage covers the event-level `session.error` form. This is
// the other: the assistant message is completed with an `error` on its info, and NO session.error
// and NO session.idle follow. Nothing closed the turn, so the daemon held it `running` forever —
// heartbeats kept reporting the session alive, which it was, and the app sat on "working…" over an
// empty transcript.
//
// Found live while smoke-testing: a turn failed one second in and the app still showed "working"
// ten minutes later. The message below is the exact frame the real server sent.
func TestMessageErrorClosesTheTurnInsteadOfHangingForever(t *testing.T) {
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
			// A completed assistant message carrying the failure — and nothing after it. No
			// session.error, no session.idle. That silence is the bug: the turn had no other way
			// to end.
			fmt.Fprintf(w, "data: {\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"m1\",\"sessionID\":\"s1\",\"role\":\"assistant\",\"time\":{\"created\":1,\"completed\":2},\"error\":{\"name\":\"APIError\",\"data\":{\"message\":%q,\"statusCode\":400}}}}}\n\n", wantMsg)
			flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"s1","directory":"/tmp"}`))
		default:
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
					t.Fatalf("detail = %q, want the provider's own words", st.Detail)
				}
				return
			}
		case <-deadline:
			t.Fatal("turn never ended — this is the ten-minute 'working…' hang")
		}
	}
}

// One outage, one error. message.updated repeats for the same message id, and a session.error can
// arrive for the same failure — without dedupe the user is shown the same outage several times and
// has to work out whether anything actually happened more than once.
func TestRepeatedMessageErrorIsSurfacedOnce(t *testing.T) {
	const wantMsg = "Upstream request failed: Model is unavailable."

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
			frame := fmt.Sprintf("data: {\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"m1\",\"sessionID\":\"s1\",\"role\":\"assistant\",\"time\":{\"created\":1,\"completed\":2},\"error\":{\"name\":\"APIError\",\"data\":{\"message\":%q}}}}}\n\n", wantMsg)
			for i := 0; i < 3; i++ {
				fmt.Fprint(w, frame)
				flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"s1","directory":"/tmp"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	errors := 0
	settle := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				goto done
			}
			if st, isStatus := ev.Payload.(protocol.SessionStatus); isStatus && st.Status == protocol.StatusError {
				errors++
			}
		case <-settle:
			goto done
		}
	}
done:
	if errors != 1 {
		t.Fatalf("surfaced %d errors for one failure, want exactly 1", errors)
	}
}

// A healthy assistant message must not be mistaken for a failed one — an empty `error` object is
// what every successful turn carries, and treating it as a failure would break every turn there is.
func TestHealthyMessageDoesNotEmitAnError(t *testing.T) {
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
			fmt.Fprint(w, "data: {\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"m1\",\"sessionID\":\"s1\",\"role\":\"assistant\",\"time\":{\"created\":1,\"completed\":2},\"cost\":0.01,\"tokens\":{\"input\":10,\"output\":5}}}}\n\n")
			flush()
			fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"s1\"}}\n\n")
			flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"s1","directory":"/tmp"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	settle := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return
			}
			if st, isStatus := ev.Payload.(protocol.SessionStatus); isStatus && st.Status == protocol.StatusError {
				t.Fatalf("a successful turn was reported as an error: %q", st.Detail)
			}
		case <-settle:
			return
		}
	}
}
