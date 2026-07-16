package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// stub mimics the subset of the opencode `serve` HTTP/SSE API the provider uses,
// with the real event shapes (message.part.updated, permission.updated, session.idle).
type stub struct {
	events    chan string
	connected chan struct{}
	permCh    chan string

	mu       sync.Mutex
	permResp string
}

func newStub() *stub {
	return &stub{
		events:    make(chan string, 16),
		connected: make(chan struct{}),
		permCh:    make(chan string, 1),
	}
}

func (s *stub) lastPermissionResponse() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permResp
}

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_test", "title": "stub session"})

	case r.Method == http.MethodGet && r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush() // flush headers so the client's Do() returns (real SSE servers do this)
		}
		select {
		case <-s.connected:
		default:
			close(s.connected)
		}
		for {
			select {
			case ev := <-s.events:
				fmt.Fprintf(w, "data: %s\n\n", ev)
				if fl != nil {
					fl.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		go s.scenario()
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/permissions/"):
		var body struct {
			Response string `json:"response"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.permResp = body.Response
		s.mu.Unlock()
		select {
		case s.permCh <- body.Response:
		default:
		}
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *stub) scenario() {
	<-s.connected // ensure an SSE reader is attached before emitting
	s.events <- `{"type":"message.part.updated","properties":{"part":{"sessionID":"ses_test"},"delta":"Hello"}}`
	s.events <- `{"type":"permission.updated","properties":{"id":"perm_1","type":"bash","sessionID":"ses_test","messageID":"msg_1","title":"run bash command","metadata":{"command":"ls"},"time":{"created":0}}}`
	<-s.permCh // wait for the client's decision
	s.events <- `{"type":"message.part.updated","properties":{"part":{"sessionID":"ses_test"},"delta":" done"}}`
	s.events <- `{"type":"session.idle","properties":{"sessionID":"ses_test"}}`
}

func TestOpenCodeProvider_E2E(t *testing.T) {
	stub := newStub()
	srv := httptest.NewServer(stub)
	defer srv.Close()

	p := New(srv.URL)
	if p.Name() != "opencode" {
		t.Fatalf("Name = %q", p.Name())
	}

	ctx := context.Background()
	sess, err := p.Create(ctx, "/repo", "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.ID() != "ses_test" {
		t.Fatalf("session id = %q", sess.ID())
	}

	var gotOutput, gotIdle bool
	var approvalID string
	timeout := time.After(5 * time.Second)

	for !(gotOutput && gotIdle) {
		select {
		case ev := <-sess.Events():
			switch ev.Type {
			case protocol.TypeOutputDelta:
				gotOutput = true
			case protocol.TypeApprovalRequest:
				ar := ev.Payload.(protocol.ApprovalRequest)
				approvalID = ar.ApprovalID
				if ar.SessionID != "ses_test" || ar.Tool == "" {
					t.Fatalf("approval req = %+v", ar)
				}
				if err := sess.Respond(ctx, approvalID, protocol.DecisionAllow); err != nil {
					t.Fatal(err)
				}
			case protocol.TypeSessionStatus:
				ss := ev.Payload.(protocol.SessionStatus)
				if ss.Status == protocol.StatusIdle || ss.Status == protocol.StatusDone {
					gotIdle = true
				}
			}
		case <-timeout:
			t.Fatalf("timeout: output=%v approval=%q idle=%v", gotOutput, approvalID, gotIdle)
		}
	}

	if approvalID == "" {
		t.Fatal("expected an approval request")
	}
	if got := stub.lastPermissionResponse(); got != "once" {
		t.Fatalf("allow must map to opencode 'once', got %q", got)
	}
}
