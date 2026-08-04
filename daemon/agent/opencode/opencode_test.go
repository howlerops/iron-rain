package opencode

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

// stub mimics the subset of the opencode `serve` HTTP/SSE API the provider uses,
// with the real event shapes (message.part.delta, permission.asked, session.idle).
type stub struct {
	events    chan string
	connected chan struct{}
	permCh    chan string
	turnDone  chan struct{} // scenario signals the turn finished; POST /message returns only then (like real opencode)

	mu          sync.Mutex
	permResp    string
	sessionDir  string // ?directory= seen on POST /session
	messageDir  string // ?directory= seen on POST /session/{id}/message
	eventDir    string // ?directory= seen on GET /event (SSE subscription)
	messageBody string // raw body of the last POST /message
}

func newStub() *stub {
	return &stub{
		events:    make(chan string, 16),
		connected: make(chan struct{}),
		permCh:    make(chan string, 1),
		turnDone:  make(chan struct{}, 1),
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
		s.mu.Lock()
		s.sessionDir = r.URL.Query().Get("directory")
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_test", "title": "stub session"})

	case r.Method == http.MethodGet && r.URL.Path == "/event":
		s.mu.Lock()
		s.eventDir = r.URL.Query().Get("directory")
		s.mu.Unlock()
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
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.messageDir = r.URL.Query().Get("directory")
		s.messageBody = string(body)
		s.mu.Unlock()
		go s.scenario()
		// Real opencode blocks the message POST until the turn finishes — return only when the
		// scenario signals done (or the request is torn down), so the adapter's POST-return idle
		// backstop fires at the right time, not immediately.
		select {
		case <-s.turnDone:
		case <-r.Context().Done():
		}
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
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"ses_test","field":"text","delta":"Hello"}}`
	s.events <- `{"type":"permission.asked","properties":{"id":"perm_1","permission":"bash","sessionID":"ses_test","patterns":["ls"],"metadata":{"command":"ls"},"tool":{"messageID":"msg_1","callID":"call_1"}}}`
	<-s.permCh // wait for the client's decision
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"ses_test","field":"text","delta":" done"}}`
	s.events <- `{"type":"session.idle","properties":{"sessionID":"ses_test"}}`
	s.turnDone <- struct{}{} // let POST /message return (turn complete)
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

func TestListHidesChildSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/session" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"parent","title":"main","time":{"updated":1000}},
			{"id":"child","title":"task","parentID":"parent","time":{"updated":2000}}
		]`))
	}))
	defer srv.Close()

	got, err := New(srv.URL).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions = %+v, want only the primary session", got)
	}
	if got[0].ID != "parent" {
		t.Fatalf("listed session = %q, want parent", got[0].ID)
	}
}

// TestListReportsSessionDirectory: GET /session reports every session the server knows, each with
// its own `directory` — a single `opencode serve` routinely holds sessions from several unrelated
// folders/worktrees (verified live vs 1.17.19). List must carry that per-session directory through,
// because discovery/takeover otherwise assumes the server's launch dir and attaches with a
// ?directory= the session doesn't live in (opencode partitions on it → silently dropped sends).
// A session with no directory (older opencode) must decode to "" so callers can fall back, not fail.
func TestListReportsSessionDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/session" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"ses_a","title":"in a worktree","directory":"/repo/worktrees/feature-x","time":{"updated":1000}},
			{"id":"ses_b","title":"no directory field","time":{"updated":2000}}
		]`))
	}))
	defer srv.Close()

	got, err := New(srv.URL).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("sessions = %+v, want 2", got)
	}
	if got[0].Cwd != "/repo/worktrees/feature-x" {
		t.Errorf("ses_a cwd = %q, want the session's own directory", got[0].Cwd)
	}
	if got[1].Cwd != "" {
		t.Errorf("ses_b cwd = %q, want \"\" when opencode reports no directory", got[1].Cwd)
	}
}

// TestOpenCode_SendsDirectory pins the Track-1.1 fix: the cwd passed to Create/Prompt
// must be forwarded to opencode as the ?directory= query param on both POST /session
// and POST /session/{id}/message, so sessions are scoped to the right folder/worktree.
func TestOpenCode_SendsDirectory(t *testing.T) {
	stub := newStub()
	srv := httptest.NewServer(stub)
	defer srv.Close()

	const dir = "/repo/worktrees/feature-x"
	sess, err := New(srv.URL).Create(context.Background(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Wait until the message POST has been observed (Prompt fires async).
	deadline := time.After(3 * time.Second)
	for {
		stub.mu.Lock()
		md := stub.messageDir
		stub.mu.Unlock()
		if md != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the message POST")
		case <-time.After(10 * time.Millisecond):
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.sessionDir != dir {
		t.Errorf("POST /session directory = %q, want %q", stub.sessionDir, dir)
	}
	if stub.messageDir != dir {
		t.Errorf("POST /message directory = %q, want %q", stub.messageDir, dir)
	}
}

// TestOpenCode_EventStreamScopedToDirectory pins the fix for a session in a project
// folder / worktree hanging with no output: opencode partitions its /event SSE stream
// by ?directory=, so the subscription MUST carry the session's directory or it silently
// receives none of that session's events (agent runs, app spins forever). Verified live:
// /event with no directory sees only heartbeats; /event?directory=<dir> sees the deltas.
func TestOpenCode_EventStreamScopedToDirectory(t *testing.T) {
	stub := newStub()
	srv := httptest.NewServer(stub)
	defer srv.Close()

	const dir = "/Users/jacob/projects/ero-v2"
	sess, err := New(srv.URL).Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// subscribe() runs during Create; wait until the /event GET has been observed.
	deadline := time.After(3 * time.Second)
	for {
		stub.mu.Lock()
		ed := stub.eventDir
		stub.mu.Unlock()
		if ed != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the /event subscription")
		case <-time.After(10 * time.Millisecond):
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.eventDir != dir {
		t.Errorf("GET /event directory = %q, want %q", stub.eventDir, dir)
	}
}

// TestOpenCode_PromptImages: PromptImages sends a text part + a "file" part carrying the
// image as a base64 data URL (opencode's multimodal format).
func TestOpenCode_PromptImages(t *testing.T) {
	stub := newStub()
	srv := httptest.NewServer(stub)
	defer srv.Close()

	sess, err := New(srv.URL).Create(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ip, ok := sess.(agent.ImagePrompter)
	if !ok {
		t.Fatal("opencode session should implement agent.ImagePrompter")
	}
	if err := ip.PromptImages(context.Background(), "what is this?",
		[]protocol.ImageAttachment{{Mime: "image/png", Data: "AAAA"}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	for {
		stub.mu.Lock()
		b := stub.messageBody
		stub.mu.Unlock()
		if b != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no /message POST observed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stub.mu.Lock()
	body := stub.messageBody
	stub.mu.Unlock()
	for _, want := range []string{`"type":"text"`, `what is this?`, `"type":"file"`, `"mime":"image/png"`, `data:image/png;base64,AAAA`} {
		if !strings.Contains(body, want) {
			t.Errorf("message body missing %q:\n%s", want, body)
		}
	}
}
