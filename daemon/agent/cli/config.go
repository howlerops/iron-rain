package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// builtin is a well-known coding-agent CLI plus how to tell it apart from an impostor.
//
// Detecting by NAME ALONE is not safe. `copilot` is the clearest example: on a machine with the AWS
// ECS deployment tool installed, a bare PATH lookup finds it and we would happily hand a coding
// prompt to `copilot -p "fix the bug"`. Generic command names collide, so an entry that is at risk
// of collision carries an `identity` marker and must PROVE it is the tool we meant.
type builtin struct {
	cfg Config
	// identity, when set, must appear (case-insensitively) in the command's --version/--help output
	// before the agent is registered. Empty = the name is distinctive enough to trust on its own.
	identity string
}

// builtins are the CLIs auto-registered when present, so a user gets extra agents with zero config.
// Users can override or extend any of these via ~/.oculus/agents.json, which always wins.
//
// Each needs a genuinely NON-INTERACTIVE invocation: an agent that drops into a TUI would sit
// forever behind the daemon's pipe. (The adapter also gives every child an empty stdin so a prompt
// gets EOF rather than blocking — belt and braces, because these flags are third-party surface that
// can change under us.)
var builtinAgents = []builtin{
	{cfg: Config{Name: "codex", Command: "codex", Args: []string{"exec", "{prompt}"}}},
	{cfg: Config{Name: "gemini", Command: "gemini", Args: []string{"-p", "{prompt}"}}},
	{cfg: Config{Name: "cursor-agent", Command: "cursor-agent", Args: []string{"-p", "{prompt}"}}},
	{cfg: Config{Name: "aider", Command: "aider", Args: []string{"--yes-always", "--no-auto-commits", "--message", "{prompt}"}}},
	// Collision-prone names: each must identify itself before we route prompts to it.
	// `copilot` is AWS's ECS tool on many machines; `amp`, `grok`, `crush` and `goose` are all
	// plausible names for unrelated binaries.
	{cfg: Config{Name: "copilot", Command: "copilot", Args: []string{"-p", "{prompt}"}}, identity: "copilot cli"},
	{cfg: Config{Name: "goose", Command: "goose", Args: []string{"run", "-t", "{prompt}"}}, identity: "goose"},
	{cfg: Config{Name: "grok", Command: "grok", Args: []string{"-p", "{prompt}"}}, identity: "grok"},
	{cfg: Config{Name: "amp", Command: "amp", Args: []string{"-x", "{prompt}"}}, identity: "amp"},
	{cfg: Config{Name: "qwen", Command: "qwen", Args: []string{"-p", "{prompt}"}}, identity: "qwen"},
	{cfg: Config{Name: "crush", Command: "crush", Args: []string{"run", "{prompt}"}}, identity: "crush"},
}

// identityTimeout bounds the probe. A tool that can't say what it is within this budget doesn't get
// registered — better a missing agent than a misrouted prompt.
const identityTimeout = 3 * time.Second

// Detect returns the built-in agents that are present AND, where a marker is declared, prove they
// are the tool we meant.
func Detect() []Config {
	var out []Config
	for _, b := range builtinAgents {
		if _, err := exec.LookPath(b.cfg.Command); err != nil {
			continue
		}
		if b.identity != "" && !identityMatches(b.cfg.Command, b.identity) {
			log.Printf("cli: skipping %q — a binary by that name is installed but doesn't identify as %q (name collision)", b.cfg.Command, b.identity)
			continue
		}
		out = append(out, b.cfg)
	}
	return out
}

// identityMatches asks a command what it is. It tries --version first (cheap, and what most CLIs
// answer fastest), then --help. stdin is /dev/null so a tool that would otherwise prompt exits
// instead of hanging the daemon's startup.
func identityMatches(command, marker string) bool {
	marker = strings.ToLower(marker)
	for _, probe := range []string{"--version", "--help"} {
		ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
		cmd := exec.CommandContext(ctx, command, probe)
		cmd.Stdin = nil // no controlling input: an interactive tool gets EOF, not a wait
		out, _ := cmd.CombinedOutput()
		cancel()
		if strings.Contains(strings.ToLower(string(out)), marker) {
			return true
		}
	}
	return false
}

// Builtins returns the well-known agent configs (whether or not installed), so callers can label a
// registered agent as an auto-detected built-in vs a user-defined custom one.
func Builtins() []Config {
	out := make([]Config, 0, len(builtinAgents))
	for _, b := range builtinAgents {
		out = append(out, b.cfg)
	}
	return out
}

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
		// Either transport qualifies: a subprocess needs a Command, an AG-UI backend needs an
		// Endpoint. An entry with neither is unusable and an entry with both is ambiguous about which
		// one to run, so both are dropped rather than guessed at.
		if c.Name == "" || (c.Command == "") == (c.Endpoint == "") {
			continue
		}
		out = append(out, c)
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
