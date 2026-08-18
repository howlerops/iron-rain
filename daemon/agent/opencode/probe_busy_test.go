package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serialStub models what opencode actually does: it runs a session SERIALLY, so while a turn is in
// flight every request scoped to that session blocks behind it. Unscoped endpoints keep answering,
// because they are served by the process rather than by the session's own queue.
type serialStub struct {
	blocked chan struct{} // closed when the turn ends; scoped requests hang until then
}

func (s *serialStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/session" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_x", "title": "stub"})
	case r.URL.Path == "/session" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ses_x"}}) // always fast
	case strings.HasPrefix(r.URL.Path, "/session/"):
		select { // scoped: queued behind the running turn
		case <-s.blocked:
			_ = json.NewEncoder(w).Encode([]any{})
		case <-r.Context().Done():
		}
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// TestProbeCallsABusySessionBusyNotUnreachable is the regression for a live incident:
//
//	turn: session ses_… closed abandoned: agent unreachable for 23m13s (reconnected 3×, still
//	failing): Get "http://127.0.0.1:57885/session/ses_…?directory=…": context deadline exceeded
//
// The agent was working fine. The probe was scoped to the session, so it queued behind that
// session's own in-flight turn and timed out — meaning the probe failed BECAUSE the turn was
// healthy and long. Reading those timeouts as absence killed the turn after ten minutes and made a
// busy session unusable. A scoped timeout must never be able to conclude "unreachable".
func TestProbeCallsABusySessionBusyNotUnreachable(t *testing.T) {
	stub := &serialStub{blocked: make(chan struct{})}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer close(stub.blocked)

	sess, err := New(srv.URL).Create(context.Background(), "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	p, ok := sess.(interface {
		Probe(context.Context) (bool, error)
	})
	if !ok {
		t.Fatal("opencode session no longer implements Prober")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	busy, err := p.Probe(ctx)
	if err != nil {
		t.Fatalf("a busy session was reported UNREACHABLE (%v) — this is the 23-minute abandonment: "+
			"the probe blocked behind the session's own turn and read that as the agent being gone", err)
	}
	if !busy {
		t.Fatal("a session with a turn still running was reported idle; the turn would be closed early")
	}
}

// TestProbeReportsUnreachableWhenTheServerIsActuallyDown: the other half. Patience must not become
// blindness — a dead server still has to be detectable.
func TestProbeReportsUnreachableWhenTheServerIsActuallyDown(t *testing.T) {
	srv := httptest.NewServer(&serialStub{blocked: make(chan struct{})})
	sess, err := New(srv.URL).Create(context.Background(), "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	srv.Close() // the server process goes away

	p := sess.(interface {
		Probe(context.Context) (bool, error)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.Probe(ctx); err == nil {
		t.Fatal("a dead server probed clean — nothing would ever declare the turn abandoned")
	}
}

// TestProbeSurvivesASaturatedServer: opencode is a single-threaded JS server, so a turn whose
// sub-agents are streaming test-suite output can stall its event loop past any HTTP deadline —
// including the unscoped one. Liveness that depends on the app being RESPONSIVE fails exactly when
// the agent is busiest, which is the worst possible time to declare it dead.
//
// A TCP dial asks a question the event loop cannot lie about: is something still listening. That
// separates "working hard" from "gone" without asking the process to do any work.
func TestProbeSurvivesASaturatedServer(t *testing.T) {
	// The event stream stays connected (it is a long-lived socket, unaffected by a busy event loop),
	// but every request/response API call hangs — the signature of a stalled single-threaded server.
	done := make(chan struct{})
	defer close(done)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_x"})
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-done:
			}
		default:
			select { // saturated: the API never answers
			case <-r.Context().Done():
			case <-done:
			}
		}
	}))
	defer srv.Close()

	sess, err := New(srv.URL).Create(context.Background(), "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	p := sess.(interface {
		Probe(context.Context) (bool, error)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	busy, err := p.Probe(ctx)
	if err != nil {
		t.Fatalf("a saturated-but-listening server was reported unreachable (%v) — this abandons a "+
			"turn precisely because its sub-agents are producing a lot of output", err)
	}
	if !busy {
		t.Fatal("a saturated server was reported idle; the turn would be closed early")
	}
}
