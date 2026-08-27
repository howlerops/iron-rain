package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Letting an agent look at, and act on, the page it is building.
//
// The daemon has no DOM. It speaks HTTP, and a dev server's HTML is inert — for any client-rendered
// app the fetched markup is `<div id="root"></div>` and everything real happens after the page's own
// JavaScript runs. So an agent cannot verify its own UI work from here, and the two obvious ways to
// give the daemon a DOM both cost a Chromium dependency for every user whether they use it or not.
//
// The app already has a browser engine. So the daemon asks it: the request goes out to connected
// clients, whichever one has that session's preview open performs the operation in its own web view,
// and the answer comes back. Same shape as an approval — broadcast, wait on a keyed waiter, time out.
//
// The limitation is narrower than it first was. The app renders the page whether or not a person has
// the preview open: if nobody does, it builds an offscreen web view for the purpose, shows nothing
// and takes no focus. What remains is that SOME app has to be connected — there is no renderer
// otherwise — and that case still refuses clearly rather than guessing, because a wrong answer about
// what a page displays is worse than no answer.
//
// Deliberately not "the daemon asks the app to open the Design sheet". That works, and it means an
// agent can throw a window in front of whatever its owner was doing — which is a capability worth
// not having.

// previewDOMTimeout bounds one DOM round trip. Generous enough for a slow page on a phone over a
// relay, short enough that an agent is not parked on a client that has gone away.
const previewDOMTimeout = 20 * time.Second

// previewDOMWaiters correlates an outstanding ask with the client's eventual answer.
type previewDOMWaiters struct {
	mu sync.Mutex
	ch map[string]chan protocol.PreviewDOMResult
}

func newPreviewDOMWaiters() *previewDOMWaiters {
	return &previewDOMWaiters{ch: map[string]chan protocol.PreviewDOMResult{}}
}

func (w *previewDOMWaiters) open(id string) chan protocol.PreviewDOMResult {
	// Buffered so a client answering after the deadline never blocks its own read loop on a waiter
	// nobody is reading any more.
	c := make(chan protocol.PreviewDOMResult, 1)
	w.mu.Lock()
	w.ch[id] = c
	w.mu.Unlock()
	return c
}

func (w *previewDOMWaiters) close(id string) {
	w.mu.Lock()
	delete(w.ch, id)
	w.mu.Unlock()
}

// resolve delivers an answer. The FIRST answer wins: several devices may have the same preview open,
// and the agent asked one question.
func (w *previewDOMWaiters) resolve(res protocol.PreviewDOMResult) {
	w.mu.Lock()
	c, ok := w.ch[res.RequestID]
	if ok {
		delete(w.ch, res.RequestID)
	}
	w.mu.Unlock()
	if ok {
		c <- res
	}
}

// askPreviewDOM performs one operation in whichever connected app has this session's preview open.
func (h *Hub) askPreviewDOM(ctx context.Context, sessionID, op, ref, value string) (string, error) {
	if h.previewDOM == nil {
		return "", fmt.Errorf("preview control is not available")
	}
	if h.preview == nil || h.preview.URL(sessionID) == "" {
		return "", fmt.Errorf("this session has no dev server running, so there is no page to inspect")
	}

	ask := protocol.PreviewDOMAsk{
		RequestID: "pdom_" + randToken(),
		SessionID: sessionID,
		Op:        op,
		Ref:       ref,
		Value:     value,
	}
	ch := h.previewDOM.open(ask.RequestID)
	defer h.previewDOM.close(ask.RequestID)

	// Broadcast rather than target a device: the daemon does not know which client, if any, has this
	// preview open. Every client sees the ask, only one can act on it, and the first answer wins.
	h.broadcast(protocol.TypePreviewDOMAsk, ask)

	select {
	case res := <-ch:
		if !res.OK {
			if res.Error != "" {
				return "", fmt.Errorf("%s", res.Error)
			}
			return "", fmt.Errorf("the preview could not complete that operation")
		}
		return res.Result, nil
	case <-time.After(previewDOMTimeout):
		// Overwhelmingly this means nobody has the page open, so say that rather than "timeout" —
		// the agent can act on the first and can do nothing with the second.
		return "", fmt.Errorf("no Iron Rain app is connected, so there is nothing that can render " +
			"this page. Open Iron Rain on any of your devices and try again")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// previewBuiltinServer is the MCP server name these tools appear under.
const previewBuiltinServer = "ironrain-preview"

// previewTools describes the tools to the agent.
//
// The descriptions say what the tools CANNOT do as plainly as what they can. An agent that believes
// it can drive a page unattended will report a verification it never performed, which is worse than
// one that knows to ask for a person.
var previewTools = []mcp.BuiltinTool{
	{
		Name: "preview_snapshot",
		Description: "Look at the page your dev server is currently rendering, as a structured outline " +
			"of its interactive and text-bearing elements, each with a ref you can pass to preview_click " +
			"or preview_fill. This reads the LIVE DOM after the page's own JavaScript has run, so it " +
			"reflects what a person actually sees. Requires the Iron Rain app to be running somewhere — " +
			"it renders the page for me, without showing anything, if nobody has the preview open. If " +
			"no app is connected it will tell you so; do not guess at the page in that case.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name: "preview_click",
		Description: "Click an element in the live preview, identified by a ref from a previous " +
			"preview_snapshot. Take a fresh snapshot afterwards to see what changed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"ref":{"type":"string","description":"an element ref from preview_snapshot, e.g. e12"}},` +
			`"required":["ref"],"additionalProperties":false}`),
	},
	{
		Name: "preview_fill",
		Description: "Type a value into an input or textarea in the live preview, identified by a ref " +
			"from a previous preview_snapshot. Fires the input and change events a framework needs to " +
			"notice the value.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"ref":{"type":"string","description":"an element ref from preview_snapshot, e.g. e12"},` +
			`"value":{"type":"string","description":"the text to type"}},` +
			`"required":["ref","value"],"additionalProperties":false}`),
	},
}

// RegisterPreviewTools installs the builtin server on the MCP manager, making the preview tools
// visible to every session's agent.
func (h *Hub) RegisterPreviewTools(mgr *mcp.Manager) {
	if mgr == nil {
		return
	}
	mgr.RegisterBuiltin(previewBuiltinServer, previewTools, h.callPreviewTool)
}

// callPreviewTool executes one of the preview tools for the calling session.
//
// The session is taken from the CONTEXT — from the bearer token the gateway resolved — never from an
// argument. An agent cannot name a session, so it cannot reach another one's page; that is not a
// check that could be got wrong, it is an input that does not exist.
func (h *Hub) callPreviewTool(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, error) {
	token := mcp.SessionTokenFrom(ctx)
	sessionID, ok := h.mcpTokens.session(token)
	if !ok {
		return nil, fmt.Errorf("these tools act on the calling session's own preview, and this call " +
			"has no session attached to it")
	}

	var p struct {
		Ref   string `json:"ref"`
		Value string `json:"value"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}

	var op string
	switch tool {
	case "preview_snapshot":
		op = "snapshot"
	case "preview_click":
		if p.Ref == "" {
			return nil, fmt.Errorf("preview_click needs a ref from a preview_snapshot")
		}
		op = "click"
	case "preview_fill":
		if p.Ref == "" {
			return nil, fmt.Errorf("preview_fill needs a ref from a preview_snapshot")
		}
		op = "fill"
	default:
		return nil, fmt.Errorf("unknown tool %q", tool)
	}

	out, err := h.askPreviewDOM(ctx, sessionID, op, p.Ref, p.Value)
	if err != nil {
		return nil, err
	}
	log.Printf("preview: %s for session %s", op, sessionID)
	return json.RawMessage(out), nil
}
