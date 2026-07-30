package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Gateway exposes the daemon's supervised MCP servers over local HTTP, so a harness connects to
// http://127.0.0.1:<port>/mcp/<name> instead of spawning its own copy.
//
// Why this shape: "a local HTTP MCP endpoint" is a config every harness already supports, which
// means the gateway works with agents that know nothing about Iron Rain. And because every request
// transits the daemon, this is the one place where a tool call can be seen — the foundation for
// auditing and, later, for applying the same approval rules to MCP tools that already gate native
// ones.
//
// Transport: JSON-RPC over POST, one response per request. The streamable-HTTP spec permits a plain
// JSON response when the server has nothing to stream, which covers initialize/tools/list/tools/call
// — the calls a coding agent actually makes.

// requestTimeout bounds one proxied call. Long enough for a real tool (a web fetch, a slow API),
// short enough that a hung server doesn't pin a harness forever.
const requestTimeout = 120 * time.Second

// maxRequestBytes bounds an inbound request body.
const maxRequestBytes = 8 << 20

// Gateway is an http.Handler serving /mcp/<server>.
type Gateway struct {
	mgr   *Manager
	token string
	// onToolCall is notified after a successful tools/call, for the activity feed. Optional.
	onToolCall func(server, tool string)
}

// NewGateway returns a gateway over mgr. token must be presented as a bearer credential: the
// endpoint listens on loopback, but loopback is shared with every other process on the machine, so
// "local" is not by itself an authorization.
func NewGateway(mgr *Manager, token string) *Gateway {
	return &Gateway{mgr: mgr, token: token}
}

// SetToolCallHook installs a callback fired after each successful tools/call.
func (g *Gateway) SetToolCallHook(f func(server, tool string)) { g.onToolCall = f }

// URLFor returns the local endpoint a harness should be pointed at.
func (g *Gateway) URLFor(base, name string) string {
	return strings.TrimRight(base, "/") + "/mcp/" + name
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/mcp/")
	if name == "" || strings.Contains(name, "/") {
		http.Error(w, "unknown MCP server", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		// GET is the pre-2026 SSE channel; we don't offer one, and saying so plainly beats a 404
		// that a client will retry forever.
		w.Header().Set("Allow", "POST")
		http.Error(w, "this gateway speaks JSON-RPC over POST", http.StatusMethodNotAllowed)
		return
	}
	if !g.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	var req struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Method  string           `json:"method"`
		Params  json.RawMessage  `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	// A notification has no id and expects no reply; forwarding it is pointless because the upstream
	// answer would have nowhere to go.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	client, protocolVersion, err := g.mgr.Get(ctx, name)
	if err != nil {
		writeRPCError(w, req.ID, -32000, err.Error())
		return
	}

	params := req.Params
	// The 2026-07-28 revision requires per-request _meta. A harness still speaking the older shape
	// won't send it, so the gateway adds it when the upstream server expects it — this is exactly
	// the version-bridging that makes one registry usable by harnesses on different revisions.
	if protocolVersion == ProtocolLatest {
		params = withMeta(params, protocolVersion)
	}

	result, callErr := client.call(ctx, req.Method, rawOrNil(params))
	if callErr != nil {
		writeRPCError(w, req.ID, -32000, callErr.Error())
		return
	}
	if req.Method == "tools/call" && g.onToolCall != nil {
		g.onToolCall(name, toolNameFrom(req.Params))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  json.RawMessage(result),
	})
}

// authorized checks the bearer token in constant time.
func (g *Gateway) authorized(r *http.Request) bool {
	if g.token == "" {
		return true // no token configured (tests / explicitly open)
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(g.token)) == 1
}

// withMeta injects the per-request _meta the newest revision requires, preserving whatever the
// caller sent.
func withMeta(params json.RawMessage, version string) json.RawMessage {
	obj := map[string]any{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &obj); err != nil {
			return params // not an object — leave it alone rather than corrupt it
		}
	}
	if _, present := obj["_meta"]; !present {
		obj["_meta"] = metaFor(version)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return params
	}
	return out
}

// rawOrNil avoids sending an explicit null params for a method that has none.
func rawOrNil(p json.RawMessage) any {
	if len(p) == 0 {
		return nil
	}
	return p
}

// toolNameFrom extracts a tools/call target for the audit trail.
func toolNameFrom(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &p)
	return p.Name
}

func writeRPCError(w http.ResponseWriter, id *json.RawMessage, code int, msg string) {
	log.Printf("mcp gateway: %s", msg)
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": code, "message": msg},
	}
	if id != nil {
		resp["id"] = id
	} else {
		resp["id"] = nil
	}
	_ = json.NewEncoder(w).Encode(resp)
}
