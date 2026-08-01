package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// attachStub is an opencode server whose session lookup can be switched off, so a test can express
// "this server is up, but it has never heard of that session" — the exact state a daemon restart
// hits when it re-attaches to the wrong opencode (or to one that was restarted itself).
type attachStub struct {
	sessionID string
	knows     bool
	messages  string        // JSON array served for GET /session/:id/message
	posted    chan []byte   // bodies POSTed to /message
	closed    chan struct{} // closed once, to release the /event handler
}

func newAttachStub(sessionID, messages string) *attachStub {
	return &attachStub{sessionID: sessionID, knows: true, messages: messages, posted: make(chan []byte, 4)}
}

func (s *attachStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	case r.Method == http.MethodPost && r.URL.Path == "/session/"+s.sessionID+"/message":
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		select {
		case s.posted <- buf:
		default:
		}
		_, _ = w.Write([]byte(`{}`))
	case r.URL.Path == "/session/"+s.sessionID+"/message":
		body := s.messages
		if body == "" {
			body = "[]"
		}
		_, _ = w.Write([]byte(body))
	case r.URL.Path == "/session/"+s.sessionID:
		if !s.knows {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": s.sessionID, "directory": "/Users/x/proj"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestAttachVerifiedFailsWhenTheServerDoesNotKnowTheSession: /event accepts ANY subscriber, so a
// plain attach against a stranger server "succeeds" and produces a session that looks live, shows no
// history, and swallows every send. AttachVerified is the restore's contract: prove the server holds
// the session (it can report its directory) or fail, so the record stays stopped/restartable.
func TestAttachVerifiedFailsWhenTheServerDoesNotKnowTheSession(t *testing.T) {
	stub := newAttachStub("ses_x", "[]")
	stub.knows = false
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	p := New(srv.URL)
	sess, err := p.AttachVerified(context.Background(), "ses_x", "/Users/x/proj")
	if err == nil {
		_ = sess.Close()
		t.Fatal("AttachVerified succeeded against a server that cannot resolve the session — the restore would present a live-looking session whose sends go nowhere")
	}
}

// TestAttachVerifiedSucceedsWhenTheServerHoldsTheSession is the other half: the strict path must not
// break the normal case, or every restart would demote healthy sessions to "stopped".
func TestAttachVerifiedSucceedsWhenTheServerHoldsTheSession(t *testing.T) {
	stub := newAttachStub("ses_x", "[]")
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	p := New(srv.URL)
	sess, err := p.AttachVerified(context.Background(), "ses_x", "")
	if err != nil {
		t.Fatalf("AttachVerified failed on a server that holds the session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if dr, ok := sess.(interface{ Dir() string }); !ok || dr.Dir() != "/Users/x/proj" {
		t.Errorf("attached session did not adopt the server's real directory")
	}
}

// TestAttachSeedsModelFromLastAssistantMessage: taking over a terminal session must not change the
// model the user was working with. opencode carries the model on every message, so an attach that
// leaves it unset sends the next turn on the server's default — silently moving a conversation from
// (say) a frontier model onto whatever is configured, mid-task, with no indication it happened.
func TestAttachSeedsModelFromLastAssistantMessage(t *testing.T) {
	history := `[
	  {"info":{"id":"m1","role":"user"},"parts":[{"type":"text","text":"hi"}]},
	  {"info":{"id":"m2","role":"assistant","providerID":"anthropic","modelID":"claude-opus-4"},"parts":[{"type":"text","text":"hello"}]}
	]`
	stub := newAttachStub("ses_x", history)
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	p := New(srv.URL)
	sess, err := p.Attach(context.Background(), "ses_x", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	// Drain the replayed history so the emit() buffer can't block the send.
	go func() {
		for range sess.Events() {
		}
	}()

	if err := sess.Prompt(context.Background(), "carry on"); err != nil {
		t.Fatal(err)
	}
	var body []byte
	select {
	case body = <-stub.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("no message POST arrived")
	}
	var sent struct {
		Model struct {
			ModelID    string `json:"modelID"`
			ProviderID string `json:"providerID"`
		} `json:"model"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode posted body %q: %v", body, err)
	}
	if sent.Model.ModelID != "claude-opus-4" || sent.Model.ProviderID != "anthropic" {
		t.Fatalf("next turn sent model %q/%q, want the session's own anthropic/claude-opus-4 — takeover switched the user's model", sent.Model.ProviderID, sent.Model.ModelID)
	}
}
