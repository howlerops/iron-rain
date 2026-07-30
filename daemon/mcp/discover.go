package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Importing MCP servers a harness is ALREADY configured with.
//
// Without this, "configure once and every agent gets it" only holds for someone starting from
// nothing — the opposite of who benefits most. Anyone with servers already set up in Claude Code or
// opencode opened an empty screen and was asked to retype all of them, and until they did, the
// daemon's registry and the harness's own config were two independent sets of servers running two
// independent sets of processes.
//
// Discovery is READ-ONLY and never auto-imports. A server definition carries a command that will run
// with the user's credentials, so it gets shown and confirmed, not silently adopted.

// Found is one server discovered in a harness's own configuration.
type Found struct {
	Server Server
	// Source is where it was found, shown to the user ("Claude Code (project)").
	Source string
	// Path is the file it came from, so the user can go turn it off there once imported.
	Path string
}

// Discover scans the harnesses' own MCP configuration for servers the daemon doesn't already have.
// cwd scopes the project-level lookups; pass "" to skip those.
func Discover(home, cwd string, existing []Server) []Found {
	have := map[string]bool{}
	for _, s := range existing {
		have[strings.ToLower(s.Name)] = true
	}

	var out []Found
	add := func(f Found) {
		if f.Server.Name == "" || have[strings.ToLower(f.Server.Name)] {
			return
		}
		have[strings.ToLower(f.Server.Name)] = true // don't offer the same name twice from two files
		out = append(out, f)
	}

	// Claude Code: project .mcp.json, then the user-level config.
	if cwd != "" {
		for _, f := range readClaudeStyle(filepath.Join(cwd, ".mcp.json"), "Claude Code (project)") {
			add(f)
		}
	}
	for _, f := range readClaudeStyle(filepath.Join(home, ".claude.json"), "Claude Code (user)") {
		add(f)
	}
	// opencode: user config, then project.
	for _, f := range readOpenCodeStyle(filepath.Join(home, ".config", "opencode", "opencode.json"), "opencode (user)") {
		add(f)
	}
	if cwd != "" {
		for _, f := range readOpenCodeStyle(filepath.Join(cwd, "opencode.json"), "opencode (project)") {
			add(f)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Server.Name < out[j].Server.Name })
	return out
}

// readClaudeStyle parses an {"mcpServers": {...}} document. ~/.claude.json also nests per-project
// server sets under "projects", which are read too — that's where `claude mcp add` puts a local-scope
// server, and it's the most common place a user's servers actually live.
func readClaudeStyle(path, source string) []Found {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		MCPServers map[string]claudeServer `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]claudeServer `json:"mcpServers"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []Found
	for name, cs := range doc.MCPServers {
		if s, ok := cs.toServer(name); ok {
			out = append(out, Found{Server: s, Source: source, Path: path})
		}
	}
	for project, p := range doc.Projects {
		for name, cs := range p.MCPServers {
			if s, ok := cs.toServer(name); ok {
				out = append(out, Found{Server: s, Source: source + " · " + filepath.Base(project), Path: path})
			}
		}
	}
	return out
}

type claudeServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func (c claudeServer) toServer(name string) (Server, bool) {
	switch {
	case c.URL != "":
		return Server{Name: name, Transport: "http", URL: c.URL, Headers: c.Headers}, true
	case c.Command != "":
		return Server{Name: name, Transport: "stdio", Command: c.Command, Args: c.Args, Env: c.Env}, true
	}
	return Server{}, false
}

// readOpenCodeStyle parses opencode's {"mcp": {...}} document, whose shape differs from everyone
// else's: `command` is the full argv ARRAY and the env map is called `environment`.
func readOpenCodeStyle(path, source string) []Found {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Environment map[string]string `json:"environment"`
			Cwd         string            `json:"cwd"`
			URL         string            `json:"url"`
			Headers     map[string]string `json:"headers"`
			Enabled     *bool             `json:"enabled"`
		} `json:"mcp"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []Found
	for name, e := range doc.MCP {
		if e.Enabled != nil && !*e.Enabled {
			continue // don't import something the user already turned off
		}
		switch {
		case e.URL != "":
			out = append(out, Found{
				Server: Server{Name: name, Transport: "http", URL: e.URL, Headers: e.Headers},
				Source: source, Path: path,
			})
		case len(e.Command) > 0:
			out = append(out, Found{
				Server: Server{
					Name: name, Transport: "stdio",
					Command: e.Command[0], Args: e.Command[1:],
					Env: e.Environment, Cwd: e.Cwd,
				},
				Source: source, Path: path,
			})
		}
	}
	return out
}
