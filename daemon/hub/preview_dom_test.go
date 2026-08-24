package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/protocol"
)

// An agent must not be able to reach another session's page. It cannot NAME a session — the tools
// take no session argument, and the identity comes from the bearer token the gateway resolved — so
// this is not a check that could be got wrong, it is an input that does not exist.
func TestPreviewToolsRefuseACallWithNoSession(t *testing.T) {
	h := New()
	_, err := h.callPreviewTool(mcp.WithSessionToken(context.Background(), "not-a-session-token"),
		"preview_snapshot", nil)
	if err == nil {
		t.Fatal("a call with no attributable session must be refused")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("the refusal should say why, got %q", err)
	}
}

// The tools act on a running dev server. Without one there is nothing to look at, and the message
// should say that rather than time out.
func TestPreviewToolsRefuseWhenNoDevServerIsRunning(t *testing.T) {
	h, token := mcpGuardHub(t)
	_, err := h.callPreviewTool(mcp.WithSessionToken(context.Background(), token), "preview_snapshot", nil)
	if err == nil {
		t.Fatal("a session with no dev server must be refused")
	}
	if !strings.Contains(err.Error(), "no dev server") {
		t.Errorf("expected an explanation about the missing dev server, got %q", err)
	}
}

func TestPreviewClickNeedsARef(t *testing.T) {
	h, token := mcpGuardHub(t)
	ctx := mcp.WithSessionToken(context.Background(), token)
	if _, err := h.callPreviewTool(ctx, "preview_click", json.RawMessage(`{}`)); err == nil {
		t.Error("click without a ref must be refused")
	}
	if _, err := h.callPreviewTool(ctx, "preview_fill", json.RawMessage(`{"value":"x"}`)); err == nil {
		t.Error("fill without a ref must be refused")
	}
}

// With a dev server running but nobody looking at it, the agent should be told to ask for a person —
// not left to infer an empty page.
func TestPreviewToolsExplainWhenNobodyIsWatching(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer up.Close()

	h := previewHub(t, up.URL)
	h.SetApprovalRulesPath(t.TempDir() + "/rules.json")
	fake := &approvalFakeSess{ch: make(chan agent.Event, 4)}
	m := newManagedSession(h, fake, sessionMeta{})
	h.mu.Lock()
	h.sessions[fake.ID()] = m
	h.mu.Unlock()
	// Point the registered preview at this session id.
	h.preview.Register(fake.ID(), "watched-app", 1)

	token := h.mcpTokens.mint()
	h.mcpTokens.bind(token, fake.ID())

	// A short deadline stands in for the full timeout; the point is the message, not the wait.
	ctx, cancel := context.WithTimeout(mcp.WithSessionToken(context.Background(), token), 300*time.Millisecond)
	defer cancel()

	_, err := h.callPreviewTool(ctx, "preview_snapshot", nil)
	if err == nil {
		t.Fatal("with no client showing the preview, this must fail rather than invent an answer")
	}
}

// The first answer wins: several devices can have the same page open, and the agent asked once.
func TestPreviewDOMWaitersTakeTheFirstAnswer(t *testing.T) {
	w := newPreviewDOMWaiters()
	ch := w.open("req-1")

	w.resolve(protocol.PreviewDOMResult{RequestID: "req-1", OK: true, Result: `{"a":1}`})
	w.resolve(protocol.PreviewDOMResult{RequestID: "req-1", OK: true, Result: `{"b":2}`}) // must not block

	select {
	case got := <-ch:
		if got.Result != `{"a":1}` {
			t.Errorf("got %q, want the first answer", got.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter was never delivered an answer")
	}
}

// An unsolicited result — a late answer, or a client inventing one — matches no waiter and is
// dropped rather than resolving something else.
func TestPreviewDOMWaitersIgnoreUnknownRequests(t *testing.T) {
	w := newPreviewDOMWaiters()
	ch := w.open("mine")
	w.resolve(protocol.PreviewDOMResult{RequestID: "someone-elses", OK: true, Result: "x"})
	select {
	case got := <-ch:
		t.Fatalf("an unrelated result resolved my waiter: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// A closed waiter must not be resolvable, or a late answer would be delivered to a caller that has
// already given up and moved on.
func TestPreviewDOMWaitersDropAfterClose(t *testing.T) {
	w := newPreviewDOMWaiters()
	_ = w.open("req-2")
	w.close("req-2")
	w.resolve(protocol.PreviewDOMResult{RequestID: "req-2", OK: true}) // must not panic
}

// The tools are declared to the agent with schemas it can call against, and the descriptions must
// state the limitation — an agent that thinks it can drive the page unattended will report a
// verification it never performed.
func TestPreviewToolsAreDeclaredHonestly(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range previewTools {
		names[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("%s has an unparseable input schema: %v", tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s schema should be an object", tool.Name)
		}
	}
	for _, want := range []string{"preview_snapshot", "preview_click", "preview_fill"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	if !strings.Contains(previewTools[0].Description, "open") {
		t.Error("preview_snapshot must tell the agent it needs the preview to be open")
	}
}

// Clicking and typing drive the user's running app and can submit a form; looking does not. The
// distinction is what read-only modes enforce.
func TestPreviewToolMutability(t *testing.T) {
	if isMutatingTool("preview_snapshot") {
		t.Error("a snapshot only reads the page")
	}
	if !isMutatingTool("preview_click") {
		t.Error("clicking can submit a form and must count as mutating")
	}
	if !isMutatingTool("preview_fill") {
		t.Error("typing into the page must count as mutating")
	}
}
