// Package cli is a generic adapter that drives ANY non-interactive coding-agent CLI as an Oculus
// provider — so users aren't limited to the three first-class integrations (claude-code, opencode,
// pi). Each configured agent (e.g. codex, gemini, aider) becomes its own provider: a prompt runs
// the agent's command in the session's working directory and streams its stdout/stderr back as
// output. It's the "bring your own agent" tier — it trades the rich signals of the native adapters
// (tool approvals, to-dos, token/cost usage) for breadth. Configure extra agents in
// ~/.oculus/agents.json; a few well-known CLIs are auto-detected from PATH.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/osctitle"
	"github.com/howlerops/oculus/daemon/procutil"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/ratelimit"
)

// Config describes one CLI agent. Args (and ResumeArgs) are templates: the tokens {prompt} and
// {cwd} are substituted per turn; if no {prompt} token appears, the prompt is appended as the last
// argument. ResumeArgs, when set, is used for every turn after the first so an agent that supports
// session continuity (e.g. `claude -c`) keeps context; otherwise each turn re-runs Args.
type Config struct {
	Name       string            `json:"name"`
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	ResumeArgs []string          `json:"resume_args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	// Models lists the model names offered for this agent (the picker); put {model} in Args to
	// substitute the chosen one (e.g. "--model {model}"). Model is the default selection.
	Models []string `json:"models,omitempty"`
	Model  string   `json:"model,omitempty"`
	// Endpoint marks this entry as an AG-UI BACKEND rather than a subprocess: the daemon POSTs runs
	// to this URL instead of spawning Command, and the agui adapter handles it. Mutually exclusive
	// with Command.
	//
	// It lives on this struct rather than in a second registry because ~/.oculus/agents.json is the
	// one file `agent.upsert` writes and the app's "manage agents" screen edits. Splitting custom
	// agents across two formats by transport would make "add an agent" mean two different things.
	Endpoint string            `json:"endpoint,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// IsAGUI reports whether this config describes an AG-UI backend rather than a subprocess.
func (c Config) IsAGUI() bool { return c.Endpoint != "" }

// Provider adapts one Config to the agent.Provider interface.
type Provider struct {
	cfg Config
	// accountEnv, when set, returns the active account's env overrides for this provider — merged
	// into each new session's process env at spawn (multi-account hot-swap).
	accountEnv func() map[string]string
}

// NewProvider wraps a Config as a provider. The provider's Name() is the config name, so it shows
// up alongside claude-code/opencode/pi in the app's agent picker.
func NewProvider(cfg Config) *Provider { return &Provider{cfg: cfg} }

// SetAccountEnv installs the resolver for the active account's env overrides (called by the hub when
// the accounts registry is wired). Applied to sessions created AFTER this is set.
func (p *Provider) SetAccountEnv(f func() map[string]string) { p.accountEnv = f }

func (p *Provider) Name() string                                     { return p.cfg.Name }
func (p *Provider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// Models returns the model names configured for this agent (empty = no picker). Put {model} in the
// agent's Args to have the chosen one substituted per turn.
func (p *Provider) Models(context.Context) ([]protocol.ModelInfo, error) {
	out := make([]protocol.ModelInfo, 0, len(p.cfg.Models))
	for _, m := range p.cfg.Models {
		out = append(out, protocol.ModelInfo{ID: m, Name: m})
	}
	return out, nil
}

func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	if p.cfg.Command == "" {
		return nil, fmt.Errorf("cli agent %q: no command configured", p.cfg.Name)
	}
	if _, err := exec.LookPath(p.cfg.Command); err != nil {
		return nil, fmt.Errorf("cli agent %q: %q not found on PATH", p.cfg.Name, p.cfg.Command)
	}
	// Snapshot the active account's env at create time so the session uses the account that was
	// active when it started (switching later affects new sessions, not running ones).
	var acctEnv map[string]string
	if p.accountEnv != nil {
		acctEnv = p.accountEnv()
	}
	// Daemon-owned MCP servers, written once for this session and substituted for {mcp_config} in
	// the agent's arg template (e.g. "--mcp-config", "{mcp_config}").
	mcpPath := ""
	if cfg, ok := mcp.FromContext(ctx); ok {
		mcpPath = cfg.CLIFile()
	}
	s := &session{
		id:            p.cfg.Name + "_" + randID(),
		cfg:           p.cfg,
		cwd:           cwd,
		model:         p.cfg.Model,
		acctEnv:       acctEnv,
		mcpConfigPath: mcpPath,
		events:        make(chan agent.Event, 64),
		out:           make(chan agent.Event, 64),
		done:          make(chan struct{}),
	}
	go s.pump()
	if strings.TrimSpace(prompt) != "" {
		s.startTurn(prompt)
	}
	return s, nil
}

type session struct {
	id     string
	cfg    Config
	cwd    string
	events chan agent.Event // public; closed by pump when the session ends
	out    chan agent.Event // internal; turn goroutines feed the pump
	done   chan struct{}

	acctEnv       map[string]string // active account's env overrides, snapshotted at create
	ratedThisTurn bool              // a rate-limit was already surfaced this turn (dedupe)

	mu        sync.Mutex
	running   bool
	turns     int
	cancel    context.CancelFunc // cancels the in-flight turn (Stop)
	closeOnce sync.Once
	model     string // selected model, substituted for {model} in Args (guarded by mu)
	// mcpConfigPath is a temp file holding the daemon's MCP servers, substituted for {mcp_config}.
	// Written once at create and reused for every turn, so an agent's own MCP state stays stable.
	mcpConfigPath string
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return s.cfg.Name }
func (s *session) Events() <-chan agent.Event { return s.events }

// MarkResumed tells a freshly-created session that it CONTINUES an earlier conversation, so its
// very first turn uses ResumeArgs (e.g. `claude -c`) instead of the cold invocation.
//
// It exists because a CLI agent keeps its continuity in the dead process's own state, not on a
// server: after a daemon restart the hub re-creates the session, the turn counter starts at zero,
// and the agent is re-run cold — a brand-new conversation displayed under the old session's history.
// The hub calls this when the session has durable turns behind it (hub/persist.go restartSession).
// No-op when the agent declares no ResumeArgs: there is nothing to resume WITH, and passing an
// agent resume flags with no prior session usually makes it fail to start.
func (s *session) MarkResumed() {
	if len(s.cfg.ResumeArgs) == 0 {
		return
	}
	s.mu.Lock()
	if s.turns == 0 {
		s.turns = 1 // the prior conversation's turns; startTurn switches templates on turns > 0
	}
	s.mu.Unlock()
}

// SetModel selects the model substituted for {model} in subsequent turns (provider unused).
func (s *session) SetModel(_, model string) error {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()
	return nil
}

// pump is the single owner/sender of the events channel: it forwards turn output and closes events
// exactly once when the session ends, so the hub's run() loop exits cleanly and no turn goroutine
// can send on a closed channel.
func (s *session) pump() {
	defer close(s.events)
	for {
		select {
		case ev := <-s.out:
			select {
			case s.events <- ev:
			case <-s.done:
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *session) emit(ev agent.Event) {
	select {
	case s.out <- ev:
	case <-s.done:
	}
}

// Prompt runs a turn. It rejects overlapping turns (a CLI agent handles one request at a time);
// the UI serializes on the resulting busy error.
func (s *session) Prompt(_ context.Context, text string) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("%s is still working — interrupt it first", s.cfg.Name)
	}
	s.mu.Unlock()
	s.startTurn(text)
	return nil
}

func (s *session) startTurn(text string) {
	s.mu.Lock()
	tmpl := s.cfg.Args
	if s.turns > 0 && len(s.cfg.ResumeArgs) > 0 {
		tmpl = s.cfg.ResumeArgs
	}
	s.turns++
	s.running = true
	model := s.model
	mcpPath := s.mcpConfigPath
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()
	go s.runTurn(ctx, substitute(tmpl, text, s.cwd, model, mcpPath))
}

func (s *session) runTurn(ctx context.Context, argv []string) {
	s.ratedThisTurn = false // fresh turn: allow one rate-limit surfacing
	s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: s.cfg.Name}})
	// A crash/non-zero exit (or a failed spawn) must end the turn as StatusError, not the default idle —
	// otherwise a wedged/failed agent reads as a clean "Finished" and the user never knows it broke.
	var turnErr error
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		if turnErr != nil {
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: s.cfg.Name + ": " + turnErr.Error()}})
		} else {
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
		}
	}()

	cmd := exec.CommandContext(ctx, s.cfg.Command, argv...)
	cmd.Dir = s.cwd
	// Account env overrides the config env (so a per-account API key wins over any default).
	cmd.Env = env(mergeEnv(s.cfg.Env, s.acctEnv))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		turnErr = err
		return
	}
	cmd.Stderr = cmd.Stdout // fold stderr into the streamed output (agents log progress there)
	// No stdin. These are third-party CLIs invoked with flags we believe are non-interactive, but
	// that is third-party surface that can change under us — and a tool that decides to prompt would
	// otherwise block forever with nobody to answer it. An empty stdin turns "hang until killed" into
	// "read EOF and exit", which surfaces as a normal failed turn the user can actually see.
	cmd.Stdin = nil
	procutil.Isolate(cmd) // a CLI agent forks compilers/test runners — Stop() must kill the tree
	if err := cmd.Start(); err != nil {
		turnErr = err
		return
	}
	s.stream(stdout, s.newTitleScanner())
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		// A non-zero exit that wasn't from our Stop(): keep the trailing line (preserves partial output)
		// AND record it so the turn ends as an error rather than a silent success.
		s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: "\n[" + s.cfg.Name + " exited: " + err.Error() + "]\n"}})
		turnErr = err
	}
}

func (s *session) stream(r io.Reader, titles *osctitle.Scanner) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// Feed the RAW bytes (before ANSI stripping) to the OSC-title scanner so an agent's
			// terminal title drives a live running/waiting/idle status — then strip for display.
			if titles != nil {
				titles.Write(buf[:n])
			}
			if txt := stripANSI(string(buf[:n])); txt != "" {
				s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: txt}})
				s.detectRateLimit(txt)
			}
		}
		if err != nil {
			return
		}
	}
}

// detectRateLimit scans agent output for a rate-limit condition (from the agent's own message) and
// surfaces it ONCE per turn as a status event with a "retry in N" hint — so the app can show "rate
// limited" and back off, on any provider, without account APIs.
func (s *session) detectRateLimit(text string) {
	if s.ratedThisTurn {
		return
	}
	info := ratelimit.Parse(text)
	if !info.Hit {
		return
	}
	s.ratedThisTurn = true
	detail := "Rate limited by the provider"
	if info.RetryAfter > 0 {
		detail += " — retry in " + info.RetryAfter.Round(time.Second).String()
	} else if info.ResetHint != "" {
		detail += " — resets at " + info.ResetHint
	}
	s.emit(agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: detail}})
}

// newTitleScanner returns an OSC-title scanner that emits a SessionStatus whenever the agent's
// terminal title implies a DIFFERENT status than last time (deduped) — giving arbitrary CLI/TUI
// agents live running/waiting/idle signal without a bespoke adapter. Called from the single stream
// goroutine, so its `last` state needs no lock.
func (s *session) newTitleScanner() *osctitle.Scanner {
	last := ""
	return osctitle.New(func(title string) {
		status := osctitle.Classify(title)
		if status == last {
			return
		}
		last = status
		detail := s.cfg.Name
		if status == osctitle.StatusRunning || status == osctitle.StatusWaiting {
			detail = title // surface what it's doing / waiting on
		}
		s.emit(agent.Event{Type: protocol.TypeSessionStatus,
			Payload: protocol.SessionStatus{SessionID: s.id, Status: status, Detail: detail}})
	})
}

// Respond is a no-op: the generic adapter has no structured tool-approval channel. Native adapters
// (claude-code/opencode/pi) are the ones that surface approvals.
func (s *session) Respond(context.Context, string, string) error { return nil }

// Probe implements agent.Prober. For the generic CLI adapter a turn IS a subprocess: it is busy for
// exactly as long as that process runs, and the process exiting closes the turn. So `running` is
// authoritative and needs no round-trip.
//
// It reports (false, nil) — idle — rather than an error when no turn is in flight, which lets the
// hub's reconciler recover a turn whose completion event was lost instead of skipping this provider
// entirely (before this existed, any session that wasn't a Prober was left to heartbeat forever).
func (s *session) Probe(context.Context) (bool, error) {
	select {
	case <-s.done:
		return false, fmt.Errorf("%s: session is closed", s.cfg.Name)
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, nil
}

// Stop cancels the in-flight turn but keeps the session alive for follow-ups.
func (s *session) Stop(context.Context) error {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

// Close ends the session for good: it stops any turn and closes the event stream (via pump).
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		path := s.mcpConfigPath
		s.mcpConfigPath = ""
		s.mu.Unlock()
		if path != "" {
			_ = os.Remove(path) // the MCP config may hold credentials — don't leave it in /tmp
		}
	})
	return nil
}

// substitute expands {prompt}/{cwd} in the arg template. If no arg contains {prompt}, the prompt is
// appended as the final argument (so a bare command like `["exec"]` still receives it).
func substitute(tmpl []string, prompt, cwd, model, mcpConfig string) []string {
	out := make([]string, 0, len(tmpl)+1)
	sawPrompt := false
	for _, a := range tmpl {
		if strings.Contains(a, "{prompt}") {
			sawPrompt = true
		}
		a = strings.ReplaceAll(a, "{prompt}", prompt)
		a = strings.ReplaceAll(a, "{cwd}", cwd)
		a = strings.ReplaceAll(a, "{model}", model)
		// {mcp_config} expands to a file holding the daemon's MCP servers, so ANY agent that accepts
		// a --mcp-config-style flag gets the same servers as the native harnesses, with no adapter.
		a = strings.ReplaceAll(a, "{mcp_config}", mcpConfig)
		out = append(out, a)
	}
	if !sawPrompt {
		out = append(out, prompt)
	}
	return out
}

func env(extra map[string]string) []string {
	e := os.Environ()
	for k, v := range extra {
		e = append(e, k+"="+v)
	}
	return e
}

// mergeEnv overlays `over` onto `base` (over wins). Either may be nil.
func mergeEnv(base, over map[string]string) map[string]string {
	if len(over) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\r`)

// stripANSI removes ANSI escape/color sequences and bare carriage returns so a non-interactive
// agent's output renders cleanly in the chat surface.
func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }
