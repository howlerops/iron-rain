// Package commands enumerates the slash commands available to an agent session, so the app can
// offer a "/" command palette like the native CLIs. It combines each provider's well-known built-in
// commands with the user's own custom commands (Markdown files under .claude/commands, etc.).
package commands

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Command is one slash command offered in the composer palette.
type Command struct {
	Name        string `json:"name"`        // without the leading slash, e.g. "compact"
	Description string `json:"description"`
	Source      string `json:"source"` // "builtin" or "custom"
}

// List returns the slash commands for a provider, scoped to a working directory (for custom
// commands). Built-ins are the documented, stable commands; custom commands are scanned from disk.
func List(provider, cwd string) []Command {
	var out []Command
	seen := map[string]bool{}
	add := func(cs ...Command) {
		for _, c := range cs {
			if c.Name == "" || seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			out = append(out, c)
		}
	}

	switch provider {
	case "claude-code":
		add(builtin(claudeBuiltins)...)
		add(scanDir(filepath.Join(cwd, ".claude", "commands"), "")...)
		if home, err := os.UserHomeDir(); err == nil {
			add(scanDir(filepath.Join(home, ".claude", "commands"), "")...)
		}
	case "opencode":
		add(builtin(opencodeBuiltins)...)
		add(scanDir(filepath.Join(cwd, ".opencode", "command"), "")...)
	default:
		// Generic CLI agents (codex/gemini/…) have no standard slash-command surface.
	}

	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Source == "builtin") != (out[j].Source == "builtin") {
			return out[i].Source == "builtin" // built-ins first
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func builtin(m [][2]string) []Command {
	cs := make([]Command, 0, len(m))
	for _, kv := range m {
		cs = append(cs, Command{Name: kv[0], Description: kv[1], Source: "builtin"})
	}
	return cs
}

// scanDir turns each *.md file under dir into a custom command (name = path relative to dir with
// slashes for namespacing, minus ".md"; description = frontmatter `description:` or the first
// non-empty, non-heading line).
func scanDir(dir, prefix string) []Command {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Command
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			out = append(out, scanDir(filepath.Join(dir, name), prefix+name+":")...)
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		cmd := prefix + strings.TrimSuffix(name, ".md")
		out = append(out, Command{Name: cmd, Description: firstLine(filepath.Join(dir, name)), Source: "custom"})
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
		if line == "---" { // frontmatter fence
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

// claudeBuiltins are Claude Code's documented built-in slash commands.
var claudeBuiltins = [][2]string{
	{"clear", "Clear the conversation history"},
	{"compact", "Summarize the conversation to free up context"},
	{"cost", "Show token usage and cost for this session"},
	{"init", "Generate a CLAUDE.md for this project"},
	{"memory", "Edit CLAUDE.md memory files"},
	{"model", "Change the model for this session"},
	{"review", "Review a pull request"},
	{"pr-comments", "Fetch comments from a pull request"},
	{"agents", "Manage custom subagents"},
	{"help", "Show available commands"},
}

// opencodeBuiltins are opencode's common slash commands.
var opencodeBuiltins = [][2]string{
	{"new", "Start a new session"},
	{"clear", "Clear the conversation"},
	{"compact", "Compact the conversation"},
	{"init", "Initialize project context"},
	{"share", "Share this session"},
	{"undo", "Undo the last change"},
	{"redo", "Redo the last change"},
	{"models", "Switch models"},
	{"help", "Show available commands"},
}
