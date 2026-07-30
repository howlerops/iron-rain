package mcp

import (
	"encoding/json"
	"os"
)

// Injection: turning ONE registry into each harness's own config format.
//
// Every harness already knows how to run MCP servers; none of them know about each other. So rather
// than asking users to configure the same server three times, the daemon renders its registry into
// whatever shape each harness expects, at spawn time. A harness needs no awareness of Iron Rain for
// this to work.
//
// The formats below are each harness's documented shape — deliberately not "close enough":
//   - Claude Agent SDK: {"name": {"type":"stdio","command","args","env"}} passed to the sidecar.
//   - opencode:         {"mcp": {"name": {"type":"local","command":[cmd, ...args],"environment"}}}
//                       note `command` is an ARRAY and the env key is `environment` — unlike everyone else.
//   - Generic CLI:      the Claude-style mcpServers document written to a temp file, which is what
//                       `--mcp-config <file>` consumes.

// ClaudeConfig renders servers in the Claude Agent SDK's mcpServers shape.
func ClaudeConfig(servers []Server) map[string]any {
	out := map[string]any{}
	for _, s := range servers {
		switch s.Transport {
		case "http":
			e := map[string]any{"type": "http", "url": s.URL}
			if len(s.Headers) > 0 {
				e["headers"] = s.Headers
			}
			out[s.Name] = e
		default:
			e := map[string]any{"type": "stdio", "command": s.Command}
			if len(s.Args) > 0 {
				e["args"] = s.Args
			}
			if len(s.Env) > 0 {
				e["env"] = s.Env
			}
			out[s.Name] = e
		}
	}
	return out
}

// ClaudeConfigJSON renders ClaudeConfig as a compact JSON document, or "" when there is nothing to
// inject (so callers can skip setting the variable entirely).
func ClaudeConfigJSON(servers []Server) string {
	if len(servers) == 0 {
		return ""
	}
	b, err := json.Marshal(ClaudeConfig(servers))
	if err != nil {
		return ""
	}
	return string(b)
}

// OpenCodeConfigJSON renders the servers as an opencode config document suitable for
// OPENCODE_CONFIG_CONTENT. opencode MERGES configs rather than replacing them, so this adds our
// servers without disturbing the user's own opencode.json.
//
// Returns "" when there is nothing to inject.
func OpenCodeConfigJSON(servers []Server) string {
	if len(servers) == 0 {
		return ""
	}
	mcp := map[string]any{}
	for _, s := range servers {
		switch s.Transport {
		case "http":
			e := map[string]any{"type": "remote", "url": s.URL, "enabled": true}
			if len(s.Headers) > 0 {
				e["headers"] = s.Headers
			}
			mcp[s.Name] = e
		default:
			// opencode takes the command as a single ARRAY (argv), and calls the env map
			// "environment" — both differ from every other harness.
			argv := append([]string{s.Command}, s.Args...)
			e := map[string]any{"type": "local", "command": argv, "enabled": true}
			if len(s.Env) > 0 {
				e["environment"] = s.Env
			}
			if s.Cwd != "" {
				e["cwd"] = s.Cwd
			}
			mcp[s.Name] = e
		}
	}
	b, err := json.Marshal(map[string]any{"mcp": mcp})
	if err != nil {
		return ""
	}
	return string(b)
}

// WriteCLIConfig writes a Claude-style mcpServers document to a temp file and returns its path, for
// CLI agents invoked with `--mcp-config <file>` (the `{mcp_config}` arg token). Returns "" when
// there is nothing to inject. The caller owns the file; it is 0600 because Env may hold credentials.
func WriteCLIConfig(servers []Server) string {
	if len(servers) == 0 {
		return ""
	}
	doc := map[string]any{"mcpServers": ClaudeConfig(servers)}
	b, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	f, err := os.CreateTemp("", "ironrain-mcp-*.json")
	if err != nil {
		return ""
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return ""
	}
	if _, err := f.Write(b); err != nil {
		return ""
	}
	return f.Name()
}
