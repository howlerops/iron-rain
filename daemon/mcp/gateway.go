package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
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

// Authorizer decides whether one tools/call may proceed. It receives the bearer token the caller
// presented — which identifies the SESSION, not just the machine — plus the server, the tool, and
// the tool's raw arguments. Returning an error blocks the call and the error text is returned to
// the agent.
//
// This is the seam that lets MCP tools obey the same approval rules and read-only modes as native
// tools. Without a per-session token an MCP call arrives with no identity at all and can only be
// allowed or refused wholesale.
//
// `args` is load-bearing, not diagnostic. Without it the decision can only be made on a tool's NAME,
// which means an approval card cannot say WHICH file a write touches or WHICH url a fetch requests,
// the path/pattern scopes cannot apply, and the .git guard has nothing to inspect — the paths a
// harness acts on live in the arguments, never in the name. An authorizer that sees only a name can
// answer "may this session ever call write?" when the question that matters is "may it write THIS".
type Authorizer func(ctx context.Context, token, server, tool string, args json.RawMessage) error

// Gateway is an http.Handler serving /mcp/<server>.
type Gateway struct {
	mgr   *Manager
	token string
	// onToolCall is notified after a successful tools/call, for the activity feed. Optional.
	onToolCall func(server, tool string)
	// authorize gates tools/call. Optional; nil means every call proceeds.
	authorize Authorizer
	// sessionTokens are per-session bearer tokens, checked in addition to the machine token.
	sessionMu     sync.RWMutex
	sessionTokens map[string]bool
}

// NewGateway returns a gateway over mgr. token must be presented as a bearer credential.
//
// This handler is NOT loopback-only, despite what the mount site suggests: the --addr flag defaults
// to 127.0.0.1, but every shipped install overrides it to 0.0.0.0:6000 so the pairing QR can carry
// the Mac's LAN IP. An earlier version of this comment asserted loopback and that assumption is
// exactly how the machine token came to be an unauthenticated remote allow-all. Treat every request
// here as potentially arriving from the network.
func NewGateway(mgr *Manager, token string) *Gateway {
	return &Gateway{mgr: mgr, token: token, sessionTokens: map[string]bool{}}
}

// SetAuthorizer installs the tools/call gate.
func (g *Gateway) SetAuthorizer(a Authorizer) { g.authorize = a }

// AddSessionToken registers a per-session bearer token.
func (g *Gateway) AddSessionToken(token string) {
	if token == "" {
		return
	}
	g.sessionMu.Lock()
	g.sessionTokens[token] = true
	g.sessionMu.Unlock()
}

// RemoveSessionToken revokes one, when its session ends.
func (g *Gateway) RemoveSessionToken(token string) {
	g.sessionMu.Lock()
	delete(g.sessionTokens, token)
	g.sessionMu.Unlock()
}

// knownSessionToken reports whether a token was issued for a live session.
func (g *Gateway) knownSessionToken(token string) bool {
	g.sessionMu.RLock()
	defer g.sessionMu.RUnlock()
	return g.sessionTokens[token]
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
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !g.authorized(bearer) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The machine-wide token carries no session, so no mode, rule or approval can be attributed to
	// it — it is an allow-all credential by construction. That is defensible for the user's own local
	// tooling and indefensible from the network, and this handler is NOT loopback-only in practice:
	// every shipped install path (the app's daemon child, both launchd agents, the installer's
	// fallback) passes --addr 0.0.0.0:6000 so the pairing QR can carry the LAN IP.
	//
	// So a single 0600 file read anywhere converts into unapproved tool execution from any host on
	// the network. Session tokens stay reachable remotely — they carry policy and are answerable to
	// it; the credential that answers to nothing does not.
	if g.isMachineToken(bearer) && !isLoopbackAddr(r.RemoteAddr) {
		http.Error(w, "the machine token is accepted on loopback only; use a session token", http.StatusForbidden)
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

	// Gate the call BEFORE connecting: a denied tool shouldn't even wake a sleeping server, and the
	// user shouldn't wait on a spawn for a call that was never going to run.
	if req.Method == "tools/call" && g.authorize != nil {
		if err := g.authorize(ctx, bearer, name, toolNameFrom(req.Params), toolArgsFrom(req.Params)); err != nil {
			writeRPCError(w, req.ID, -32001, err.Error())
			return
		}
	}

	client, protocolVersion, err := g.mgr.Dial(ctx, name)
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

// isMachineToken reports whether the presented credential is the machine-wide one rather than a
// per-session token. Constant-time, since a mismatch here decides whether a remote caller is turned
// away.
func (g *Gateway) isMachineToken(bearer string) bool {
	if g.token == "" {
		return false // no machine token configured; nothing to confine
	}
	return subtle.ConstantTimeCompare([]byte(bearer), []byte(g.token)) == 1
}

// isLoopbackAddr reports whether a RemoteAddr belongs to this machine.
//
// A malformed address is treated as NOT loopback: this decides whether to grant the allow-all
// credential, so anything unparseable must fail closed.
func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote // some transports hand over a bare address
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// authorized accepts either the machine-wide token or a live per-session token. Comparison against
// the machine token is constant-time; session tokens are random 128-bit values looked up by exact
// match, so there is no secret to leak through timing.
func (g *Gateway) authorized(bearer string) bool {
	if g.token == "" {
		return true // no token configured (tests / explicitly open)
	}
	if subtle.ConstantTimeCompare([]byte(bearer), []byte(g.token)) == 1 {
		return true
	}
	return g.knownSessionToken(bearer)
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

// toolArgsFrom extracts the tool's arguments object, which is what the tool will actually act on.
//
// Returns nil rather than an error for a malformed or absent params: the authorizer must still run
// on a call whose arguments could not be parsed. Failing open on the ARGUMENTS while still enforcing
// on the NAME is the safe asymmetry — a call the guard cannot read is one it cannot clear either,
// and it will fall through to a real approval rather than being silently allowed.
func toolArgsFrom(params json.RawMessage) json.RawMessage {
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	return p.Arguments
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
