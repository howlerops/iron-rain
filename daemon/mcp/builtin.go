package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// A daemon-provided MCP server: tools the daemon answers itself, rather than proxying to a process.
//
// Everything else here is a HOST over external servers — the registry holds commands and URLs, and a
// tool exists only because some server answered tools/list. There was no way to offer a tool the
// daemon implements, and a tool that has to reach the daemon's own state (which session is calling,
// what its preview is, which client is showing it) cannot live in a separate process without
// exporting all of that first.
//
// So the builtin plugs in at the Caller seam, which is the narrowest place it can: the gateway still
// authorizes, still applies modes and approval rules, still logs. From the gateway's side this is
// indistinguishable from any other server.
//
// It lives in this package because Caller.call is unexported — deliberately, since implementing it
// means being proxied to without further checks.

// BuiltinTool is one tool the daemon implements, as it should appear in tools/list.
type BuiltinTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// BuiltinFunc executes one tools/call. It receives the context the gateway built, which carries the
// caller's session (see WithSessionToken) — that is what lets a builtin scope its work to the
// session that asked, rather than trusting an argument to say who is calling.
type BuiltinFunc func(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, error)

// sessionTokenKey carries the presented bearer token down to a builtin.
type sessionTokenKeyType struct{}

var sessionTokenKey sessionTokenKeyType

// WithSessionToken attaches the caller's bearer token to a context.
func WithSessionToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionTokenKey, token)
}

// SessionTokenFrom recovers the bearer token a call arrived with, "" if absent.
func SessionTokenFrom(ctx context.Context) string {
	s, _ := ctx.Value(sessionTokenKey).(string)
	return s
}

// builtin is a Caller backed by a Go function.
type builtin struct {
	name  string
	tools []BuiltinTool
	fn    BuiltinFunc
}

// RegisterBuiltin installs a daemon-implemented server under `name`, shadowing any registry entry of
// the same name. Safe to call before or after the gateway is serving.
func (m *Manager) RegisterBuiltin(name string, tools []BuiltinTool, fn BuiltinFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.builtins == nil {
		m.builtins = map[string]*builtin{}
	}
	m.builtins[name] = &builtin{name: name, tools: tools, fn: fn}
}

// Builtins returns the registered builtin server names.
func (m *Manager) Builtins() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.builtins))
	for n := range m.builtins {
		out = append(out, n)
	}
	return out
}

// lookupBuiltin returns the builtin registered under name, if any.
func (m *Manager) lookupBuiltin(name string) (*builtin, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.builtins[name]
	return b, ok
}

// call answers the three methods a coding agent actually uses. Anything else gets a plain error
// rather than being forwarded, because there is nothing behind this to forward to.
func (b *builtin) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	switch method {
	case "initialize":
		return json.Marshal(map[string]any{
			"protocolVersion": ProtocolLatest,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": b.name, "version": "1"},
		})

	case "tools/list":
		return json.Marshal(map[string]any{"tools": b.tools})

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if raw, ok := params.(json.RawMessage); ok {
			_ = json.Unmarshal(raw, &p)
		}
		if p.Name == "" {
			return nil, fmt.Errorf("tools/call needs a tool name")
		}
		out, err := b.fn(ctx, p.Name, p.Arguments)
		if err != nil {
			// An MCP tool error is reported IN the result with isError, not as a protocol error: a
			// failed tool is a normal outcome the agent should read and react to, whereas a protocol
			// error suggests the server itself is broken.
			return json.Marshal(map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			})
		}
		return json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(out)}},
		})

	case "notifications/initialized", "ping":
		return json.RawMessage(`{}`), nil
	}
	return nil, fmt.Errorf("builtin server %q does not implement %s", b.name, method)
}
