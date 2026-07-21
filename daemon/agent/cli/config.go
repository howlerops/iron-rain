package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
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
