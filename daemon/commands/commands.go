// Package commands enumerates the slash commands available to an agent session, per provider, so
// the app can offer a "/" (and, for codex, "$") command palette like the native CLIs. It combines
// each provider's well-known built-in commands with the user's own custom commands scanned from the
// provider's conventional directory (.claude/commands, ~/.codex/prompts, …).
package commands

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Command is one command offered in the composer palette. Prefix is the character it's invoked with
// ("/" for most; codex also has "$" commands).
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"` // "builtin" or "custom"
	Prefix      string `json:"prefix,omitempty"` // "/" (default) or "$"
}

// customDir is a directory scanned for custom commands, with the prefix they're invoked by.
type customDir struct {
	dirs   []string
	prefix string
}

// List returns the commands for a provider, scoped to a working directory (for custom commands).
func List(provider, cwd string) []Command {
	seen := map[string]bool{} // keyed by prefix+name so "/x" and "$x" can coexist
	var out []Command
	add := func(cs ...Command) {
		for _, c := range cs {
			if c.Name == "" {
				continue
			}
			if c.Prefix == "" {
				c.Prefix = "/"
			}
			key := c.Prefix + c.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}

	add(builtins[provider]...)
	home, _ := os.UserHomeDir()
	for _, cd := range customDirsFor(provider, cwd, home) {
		for _, dir := range cd.dirs {
			add(scanDir(dir, "", cd.prefix)...)
		}
	}
	if provider == "codex" { // codex Skills are invoked with "$"
		for _, dir := range existing(filepath.Join(cwd, ".codex", "skills"), filepath.Join(home, ".codex", "skills")) {
			add(scanSkills(dir)...)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return out[i].Prefix < out[j].Prefix // "$" before "/" (ASCII), grouped
		}
		if (out[i].Source == "builtin") != (out[j].Source == "builtin") {
			return out[i].Source == "builtin"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// customDirsFor returns the provider's custom-command directories (project-local + global).
func customDirsFor(provider, cwd, home string) []customDir {
	switch provider {
	case "claude-code":
		return []customDir{{dirs: existing(filepath.Join(cwd, ".claude", "commands"), filepath.Join(home, ".claude", "commands")), prefix: "/"}}
	case "opencode":
		return []customDir{{dirs: existing(filepath.Join(cwd, ".opencode", "command"), filepath.Join(home, ".config", "opencode", "command")), prefix: "/"}}
	case "codex":
		// Codex custom prompts (invoked with "/"): ~/.codex/prompts and a project .codex/prompts.
		return []customDir{{dirs: existing(filepath.Join(cwd, ".codex", "prompts"), filepath.Join(home, ".codex", "prompts")), prefix: "/"}}
	default:
		return nil
	}
}

func existing(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// scanDir turns each *.md file under dir into a custom command (name = path relative to dir with
// ":" for namespacing, minus ".md"; description = frontmatter `description:` or the first content line).
func scanDir(dir, prefix, cmdPrefix string) []Command {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Command
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			out = append(out, scanDir(filepath.Join(dir, name), prefix+name+":", cmdPrefix)...)
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		out = append(out, Command{
			Name:        prefix + strings.TrimSuffix(name, ".md"),
			Description: firstLine(filepath.Join(dir, name)),
			Source:      "custom",
			Prefix:      cmdPrefix,
		})
	}
	return out
}

// scanSkills lists codex Skills (each a subdirectory containing SKILL.md) as "$" commands.
func scanSkills(dir string) []Command {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Command
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skill := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(skill); err != nil {
			continue
		}
		out = append(out, Command{Name: e.Name(), Description: firstLine(skill), Source: "custom", Prefix: "$"})
	}
	return out
}

func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inFrontmatter := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "---" {
			inFrontmatter = !inFrontmatter
			continue
		}
		if inFrontmatter {
			if d, ok := strings.CutPrefix(line, "description:"); ok {
				return strings.TrimSpace(strings.Trim(strings.TrimSpace(d), `"'`))
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > 120 {
			line = line[:120] + "…"
		}
		return line
	}
	return ""
}

// builtins maps each provider to its documented built-in commands.
var builtins = map[string][]Command{
	"claude-code": {
		{Name: "clear", Description: "Clear the conversation history"},
		{Name: "compact", Description: "Summarize the conversation to free up context"},
		{Name: "cost", Description: "Show token usage and cost"},
		{Name: "init", Description: "Generate a CLAUDE.md for this project"},
		{Name: "memory", Description: "Edit CLAUDE.md memory files"},
		{Name: "model", Description: "Change the model"},
		{Name: "agents", Description: "Manage custom subagents"},
		{Name: "mcp", Description: "Manage MCP servers"},
		{Name: "review", Description: "Review a pull request"},
		{Name: "pr-comments", Description: "Fetch pull-request comments"},
		{Name: "config", Description: "Open the settings"},
		{Name: "status", Description: "Show account + system status"},
		{Name: "doctor", Description: "Diagnose the installation"},
		{Name: "help", Description: "Show available commands"},
	},
	"opencode": {
		{Name: "new", Description: "Start a new session"},
		{Name: "clear", Description: "Clear the conversation"},
		{Name: "compact", Description: "Compact the conversation"},
		{Name: "init", Description: "Initialize project context (AGENTS.md)"},
		{Name: "share", Description: "Share this session"},
		{Name: "unshare", Description: "Stop sharing this session"},
		{Name: "undo", Description: "Undo the last change"},
		{Name: "redo", Description: "Redo the last change"},
		{Name: "models", Description: "Switch models"},
		{Name: "sessions", Description: "List / switch sessions"},
		{Name: "editor", Description: "Open the external editor"},
		{Name: "help", Description: "Show available commands"},
	},
	"codex": {
		// Slash commands (per OpenAI's Codex CLI reference). Skills are invoked with "$" and are
		// scanned from disk (see scanSkills) rather than listed here.
		{Name: "model", Description: "Switch models"},
		{Name: "permissions", Description: "Adjust approvals + sandbox access"},
		{Name: "new", Description: "Start a fresh chat"},
		{Name: "resume", Description: "Continue a previous session"},
		{Name: "init", Description: "Generate an AGENTS.md scaffold"},
		{Name: "compact", Description: "Summarize the chat to conserve tokens"},
		{Name: "diff", Description: "Show git changes"},
		{Name: "review", Description: "Request working-tree analysis"},
		{Name: "mention", Description: "Attach files to the chat"},
		{Name: "status", Description: "Show session configuration"},
		{Name: "mcp", Description: "List MCP tools"},
		{Name: "skills", Description: "Browse + select task-specific skills"},
		{Name: "plan", Description: "Activate plan mode"},
		{Name: "goal", Description: "Set a persistent task target"},
		{Name: "agent", Description: "Switch agent threads"},
		{Name: "apps", Description: "Browse + attach connectors"},
		{Name: "hooks", Description: "View + control lifecycle hooks"},
		{Name: "ps", Description: "Monitor background terminals"},
		{Name: "stop", Description: "Cancel background work"},
		{Name: "fork", Description: "Branch the current session"},
		{Name: "copy", Description: "Copy the latest response"},
		{Name: "rename", Description: "Rename the session"},
		{Name: "clear", Description: "Reset and start a fresh chat"},
		{Name: "quit", Description: "Exit the CLI"},
	},
	"pi": {
		{Name: "help", Description: "Show available commands"},
		{Name: "clear", Description: "Clear the conversation"},
		{Name: "model", Description: "Switch models"},
		{Name: "reset", Description: "Reset the session"},
	},
	"gemini": {
		{Name: "help", Description: "Show available commands"},
		{Name: "clear", Description: "Clear the screen + context"},
		{Name: "compress", Description: "Compress the context into a summary"},
		{Name: "chat", Description: "Save / resume a conversation"},
		{Name: "memory", Description: "Manage GEMINI.md memory"},
		{Name: "stats", Description: "Show session stats"},
		{Name: "tools", Description: "List available tools"},
		{Name: "mcp", Description: "List MCP servers"},
		{Name: "theme", Description: "Change the theme"},
		{Name: "editor", Description: "Set the external editor"},
		{Name: "quit", Description: "Exit"},
	},
	"aider": {
		{Name: "add", Description: "Add files to the chat"},
		{Name: "drop", Description: "Remove files from the chat"},
		{Name: "diff", Description: "Show changes since the last message"},
		{Name: "undo", Description: "Undo the last aider commit"},
		{Name: "commit", Description: "Commit edits made outside the chat"},
		{Name: "run", Description: "Run a shell command"},
		{Name: "test", Description: "Run a shell command + add output on non-zero exit"},
		{Name: "model", Description: "Switch the model"},
		{Name: "tokens", Description: "Report token usage"},
		{Name: "help", Description: "Show available commands"},
	},
	"cursor-agent": {
		{Name: "model", Description: "Switch the model"},
		{Name: "help", Description: "Show available commands"},
	},
}
