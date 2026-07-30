package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// builtins are well-known coding-agent CLIs with a stable non-interactive mode. Each is registered
// only if its command is found on PATH, so a user gets extra agents automatically with zero config.
// Users can override or extend these via ~/.oculus/agents.json.
var builtins = []Config{
	{Name: "codex", Command: "codex", Args: []string{"exec", "{prompt}"}},
	{Name: "gemini", Command: "gemini", Args: []string{"-p", "{prompt}"}},
	{Name: "cursor-agent", Command: "cursor-agent", Args: []string{"-p", "{prompt}"}},
	{Name: "aider", Command: "aider", Args: []string{"--yes-always", "--no-auto-commits", "--message", "{prompt}"}},
	// The rest of the field. Each needs a genuinely NON-INTERACTIVE invocation — an agent that
	// drops into a TUI would hang forever behind the daemon's pipe, so a wrong flag here is worse
	// than a missing entry. Users can correct any of these in ~/.oculus/agents.json, which wins.
	{Name: "copilot", Command: "copilot", Args: []string{"-p", "{prompt}"}},
	{Name: "goose", Command: "goose", Args: []string{"run", "-t", "{prompt}"}},
	{Name: "grok", Command: "grok", Args: []string{"-p", "{prompt}"}},
	{Name: "amp", Command: "amp", Args: []string{"-x", "{prompt}"}},
	{Name: "qwen", Command: "qwen", Args: []string{"-p", "{prompt}"}},
	{Name: "crush", Command: "crush", Args: []string{"run", "{prompt}"}},
}

// Detect returns the built-in agents whose command is present on PATH.
func Detect() []Config {
	var out []Config
	for _, c := range builtins {
		if _, err := exec.LookPath(c.Command); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// Builtins returns the well-known agent configs (whether or not installed), so callers can label a
// registered agent as an auto-detected built-in vs a user-defined custom one.
func Builtins() []Config { return append([]Config(nil), builtins...) }

// Available reports whether a command resolves on PATH (or is an absolute/relative path that exists).
func Available(command string) bool {
	if command == "" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}

// Save writes the user-defined agents to path as a JSON array (0600). Entries with an empty name or
// command are dropped. Writing an empty slice leaves a valid empty array.
func Save(path string, cfgs []Config) error {
	out := make([]Config, 0, len(cfgs))
	for _, c := range cfgs {
		if c.Name != "" && c.Command != "" {
			out = append(out, c)
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	// Don't assume ~/.oculus already exists — this used to work only because daemon startup happened
	// to create it first, which left the writer broken for any other caller (and for tests).
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads user-defined agents from a JSON array at path. A missing file is not an error (returns
// nil). Entries with an empty name or command are skipped.
func Load(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfgs []Config
	if err := json.Unmarshal(data, &cfgs); err != nil {
		return nil, err
	}
	out := cfgs[:0]
	for _, c := range cfgs {
		if c.Name != "" && c.Command != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// Merge combines detected built-ins with user config, letting a user entry override a built-in of
// the same name. Order is preserved (built-ins first, then new user agents).
func Merge(detected, user []Config) []Config {
	byName := map[string]int{}
	out := make([]Config, 0, len(detected)+len(user))
	for _, c := range detected {
		byName[c.Name] = len(out)
		out = append(out, c)
	}
	for _, c := range user {
		if i, ok := byName[c.Name]; ok {
			out[i] = c // user overrides the built-in
			continue
		}
		byName[c.Name] = len(out)
		out = append(out, c)
	}
	return out
}
