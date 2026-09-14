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
	"github.com/howlerops/oculus/daemon/textutil"
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
	// DropLinePrefixes hides output lines a specific agent emits as diagnostics rather than answer.
	//
	// stderr is deliberately folded into the streamed output (see runTurn) because many CLI agents
	// report progress there, and dropping it would make a failing agent silent. The cost is that an
	// agent which also logs startup chatter puts it in the user's chat: gemini prefixes every run
	// with "[STARTUP] Phase 'cleanup_ops' was started but never ended…", so its answer arrived with
	// two lines of noise in front of it. Per-agent and opt-in, so the generic path is unchanged and
	// no agent's real output is filtered on a guess.
	DropLinePrefixes []string `json:"drop_line_prefixes,omitempty"`
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
		s.startTurn(prompt) // a session this new cannot already be running, so the claim always wins
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

	// tail holds the last few non-empty output lines of the CURRENT turn, so a non-zero exit can
	// report WHY it failed instead of only that it did. Written by stream() and read by runTurn()
	// on the same goroutine — stream() is called synchronously before cmd.Wait() — so no lock.
	tail []string

	mu      sync.Mutex
	running bool
	turns   int
	cancel  context.CancelFunc // cancels the in-flight turn (Stop)
	// cmd is the in-flight turn's process, kept so Stop/Close can kill its whole GROUP.
	//
	// cancel() alone is not enough and never was. procutil's own doc says what cancellation does:
	// exec.CommandContext kills the direct child and leaves the grandchildren running. Isolate then
	// puts the child in its own process group, so the daemon's group signals do not reach it either.
	// A CLI agent's turn is mostly grandchildren — `npm test`, `cargo build`, a dev server — and
	// every interrupted turn left them running, reparented to launchd, holding CPU and ports and
	// invisible to the daemon. claudecode already called TerminateGroup for exactly this reason and
	// its comment records the measurement: 143 orphaned sidecars, 284 processes, 12.7 GB resident.
	cmd       *exec.Cmd
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
	// The claim happens inside startTurn, under ONE lock.
	//
	// This used to sample `running`, drop the lock, return, and then call a startTurn that claimed
	// unconditionally — so two prompts arriving on an idle session both saw it free, both were told
	// they had been accepted, and both spawned a subprocess. The second overwrote s.cancel, which left
	// the first process running with nothing holding its handle: Stop could no longer reach it, and it
	// kept writing output into the same session. Two agents answering one conversation, from a
	// function whose only job was to prevent exactly that.
	if !s.startTurn(text) {
		return fmt.Errorf("%s is still working — interrupt it first", s.cfg.Name)
	}
	return nil
}

// startTurn claims the session and launches one turn. It reports false — having done nothing — if a
// turn is already in flight, so the claim and the check cannot be separated by a caller.
func (s *session) startTurn(text string) bool {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return false
	}
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
	return true
}

// streamDrainGrace is how long the output drain may keep reading after the agent process has been
// reaped. It exists for the ordinary case — the agent's last line is still in the pipe when it exits,
// and cutting the read there would drop the one sentence that explains a failed turn (see exitHint).
// Nothing but an inherited write end held by a process the agent deliberately backgrounded can
// outlast it, and that is exactly what this bound is here to stop waiting for.
const streamDrainGrace = time.Second

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
	// OUR OWN pipe, not cmd.StdoutPipe(). The read end has to outlive cmd.Wait() here: the turn ends
	// when the AGENT exits, and the only way to learn that without blocking on the pipe is to Wait
	// first and drain second — but Wait unconditionally closes the read end of a StdoutPipe as it
	// returns, which would discard whatever the agent wrote just before exiting. Owning the pipe
	// means we decide when that fd closes, so the drain gets a grace period first (see below).
	pr, pw, err := os.Pipe()
	if err != nil {
		turnErr = err
		return
	}
	defer pr.Close()
	cmd.Stdout = pw
	cmd.Stderr = pw // fold stderr into the streamed output (agents log progress there)
	// No stdin. These are third-party CLIs invoked with flags we believe are non-interactive, but
	// that is third-party surface that can change under us — and a tool that decides to prompt would
	// otherwise block forever with nobody to answer it. An empty stdin turns "hang until killed" into
	// "read EOF and exit", which surfaces as a normal failed turn the user can actually see.
	cmd.Stdin = nil
	procutil.Isolate(cmd) // a CLI agent forks compilers/test runners — Stop() must kill the tree
	startErr := cmd.Start()
	// Drop the parent's copy of the write end either way: while we hold it, the pipe can never reach
	// EOF, so a successful turn would hang on its own plumbing.
	pw.Close()
	if startErr != nil {
		turnErr = startErr
		return
	}
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.cmd == cmd {
			s.cmd = nil
		}
		s.mu.Unlock()
	}()
	s.tail = nil
	// Drain on its own goroutine and reap FIRST. It used to be the other way round — stream() to EOF,
	// then cmd.Wait() — which meant the turn ended when the last holder of stdout closed it, not when
	// the agent exited. Those are the same moment only if the agent forked nothing that outlives it.
	// An agent that backgrounds a process (`npm run dev &`, a watcher, a server it means to leave
	// running) hands that child the inherited stdout, so EOF never arrives: stream() blocks forever,
	// cmd.Wait() is never reached, `running` stays true, and Probe — which answers from `running` and
	// is treated as authoritative liveness — tells the turn engine's reconciler the session is fine.
	// The turn then sits "working" until the daemon restarts, with no recovery path.
	//
	// cmd.Wait() returns as soon as the AGENT is reaped; a grandchild holding the pipe cannot delay
	// it, because exec waits on the direct child only.
	streamed := make(chan struct{})
	go func() {
		defer close(streamed)
		s.stream(pr, s.newTitleScanner())
	}()
	waitErr := cmd.Wait()
	// The agent is gone. Give the drain a moment to pick up whatever is still sitting in the pipe —
	// output the agent wrote immediately before exiting, which is where its error message lives — and
	// then close the read end so a surviving grandchild's open write end cannot hold the turn open.
	// stream() unblocks on ErrFileClosed and returns; joining it before we read s.tail below keeps
	// that field owned by one goroutine at a time.
	select {
	case <-streamed:
	case <-time.After(streamDrainGrace):
		pr.Close()
		<-streamed
	}
	if err := waitErr; err != nil && ctx.Err() == nil {
		// A non-zero exit that wasn't from our Stop(): keep the trailing line (preserves partial output)
		// AND record it so the turn ends as an error rather than a silent success.
		s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: "\n[" + s.cfg.Name + " exited: " + err.Error() + "]\n"}})
		// Carry the agent's own last words into the turn's error, because an exit code alone sends
		// people to debug the wrong thing. Both of these were real: gemini exits 41 having printed
		// "you must specify the GEMINI_API_KEY environment variable", and codex exits 1 having
		// printed an ENOENT for its own missing vendored binary. What reached the fleet card, the
		// needs-you row and the push notification was "gemini: exit status 41" — an exit code the
		// user cannot look up, attached to a session that looks broken for no stated reason, while
		// the one sentence that explains it sat in the transcript.
		turnErr = err
		if why := s.exitHint(); why != "" {
			turnErr = fmt.Errorf("%w — %s", err, why)
		}
	}
}

// exitHint is what the agent said before it died, or "" if it said nothing usable.
//
// It JOINS the surviving lines rather than picking one, because the useful message is often split
// across two: gemini prints the diagnosis and the instruction separately, and taking only the last
// line yields "Update your environment and try again" — which does not say which variable is
// missing, i.e. the one fact the user needs. Bounded in length because this string ends up in a push
// notification and on a fleet card; the full output is in the transcript, which is where someone
// goes next.
func (s *session) exitHint() string {
	const maxHint = 160
	var keep []string
	for _, raw := range s.tail {
		// An INDENTED line is a continuation or a stack frame — "at ChildProcess._handle.onexit
		// (node:internal/child_process:286:19)", "errno: -2,". Those are what make a joined hint
		// unreadable, and they never carry the diagnosis; the unindented line above them does.
		if raw != strings.TrimLeft(raw, " \t") {
			continue
		}
		line := strings.TrimSpace(raw)
		// Too short to be a sentence (a bare "}"), or our own exit marker being read back.
		if len(line) < 12 || strings.HasPrefix(line, "["+s.cfg.Name+" exited:") {
			continue
		}
		keep = append(keep, line)
	}
	return textutil.Clip(strings.Join(keep, " "), maxHint)
}

// recordTail keeps the last few non-empty lines seen this turn. Deliberately small: this is a
// diagnostic of last resort, not a second copy of the transcript.
func (s *session) recordTail(txt string) {
	const keep = 6
	for _, line := range strings.Split(txt, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.tail = append(s.tail, line)
	}
	if len(s.tail) > keep {
		s.tail = s.tail[len(s.tail)-keep:]
	}
}

func (s *session) stream(r io.Reader, titles *osctitle.Scanner) {
	buf := make([]byte, 4096)
	// Holds a trailing partial line between reads, so a prefix split across two chunks is still
	// recognised. Only used when the agent declares noise to drop.
	var carry string
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// Feed the RAW bytes (before ANSI stripping) to the OSC-title scanner so an agent's
			// terminal title drives a live running/waiting/idle status — then strip for display.
			if titles != nil {
				titles.Write(buf[:n])
			}
			if txt := stripANSI(string(buf[:n])); txt != "" {
				if txt = s.dropNoise(txt, &carry); txt == "" {
					continue
				}
				s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: txt}})
				s.detectRateLimit(txt)
				s.recordTail(txt)
			}
		}
		if err != nil {
			// Flush whatever is held back. dropNoise withholds a trailing partial line so a prefix
			// split across reads is still recognised — but output need not end with a newline, and
			// without this the agent's LAST line is silently discarded. That is how the filter first
			// presented: gemini's "OK" arrived with no trailing newline and the turn came back empty.
			if carry != "" {
				if txt := s.dropNoiseFinal(carry); txt != "" {
					s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: txt}})
					s.detectRateLimit(txt)
					s.recordTail(txt)
				}
				carry = ""
			}
			return
		}
	}
}

// dropNoiseFinal judges the last, newline-less line at end of stream.
func (s *session) dropNoiseFinal(line string) string {
	for _, p := range s.cfg.DropLinePrefixes {
		if strings.HasPrefix(strings.TrimSpace(line), p) {
			return ""
		}
	}
	return line
}

// dropNoise removes whole lines matching the agent's declared diagnostic prefixes.
//
// Line-oriented, but the stream arrives in arbitrary chunks, so an incomplete trailing line is held
// in `carry` until its newline shows up — otherwise a prefix split across a read boundary would slip
// through. Returns "" when everything in this chunk was filtered.
//
// A no-op (and allocation-free) for agents that declare nothing, which is all of them but gemini.
func (s *session) dropNoise(txt string, carry *string) string {
	if len(s.cfg.DropLinePrefixes) == 0 {
		return txt
	}
	data := *carry + txt
	*carry = ""
	// Anything after the last newline is incomplete — hold it rather than judge it.
	if i := strings.LastIndexByte(data, '\n'); i >= 0 {
		*carry = data[i+1:]
		data = data[:i+1]
	} else {
		*carry = data
		return ""
	}
	var out strings.Builder
	for _, line := range strings.SplitAfter(data, "\n") {
		if line == "" {
			continue
		}
		drop := false
		for _, p := range s.cfg.DropLinePrefixes {
			if strings.HasPrefix(strings.TrimSpace(line), p) {
				drop = true
				break
			}
		}
		if !drop {
			out.WriteString(line)
		}
	}
	return out.String()
}

// detectRateLimit scans agent output for a rate-limit condition (from the agent's own message) and
// surfaces it ONCE per turn with a "retry in N" hint — so the app can show "rate limited" and back
// off, on any provider, without account APIs.
//
// PER LINE, not per read. ratelimit.Parse documents itself as inspecting "one line" and this was
// handing it whole 4 KiB chunks, so a match anywhere in the chunk was attributed to the whole of it.
//
// And NOT StatusError. turnOnStatus treats any adapter error status as a terminal turn failure: it
// closes the turn, publishes a verdict and fires a push notification. Two ordinary things then went
// badly wrong. An agent answering a question ABOUT rate limits — the user asks why an endpoint
// returns 429 — had its turn killed mid-sentence while the subprocess kept streaming into a turn the
// hub considered over. And an agent that prints "Rate limit exceeded, retrying in 30s" and then
// SUCCEEDS paged the user about a failure that never happened, with the real completion arriving
// after the turn was already closed.
//
// A rate limit is a reason the turn is SLOW, not a reason it is over. If the agent really cannot
// proceed it exits, and the non-zero exit path below reports that as the error it is.
func (s *session) detectRateLimit(text string) {
	if s.ratedThisTurn {
		return
	}
	var info ratelimit.Info
	for _, line := range strings.Split(text, "\n") {
		if got := ratelimit.Parse(line); got.Hit {
			info = got
			break
		}
	}
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
		Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: detail}})
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
	c, cmd := s.cancel, s.cmd
	s.mu.Unlock()
	// The GROUP first. Cancelling only kills the agent process itself and leaves whatever it forked
	// — the test runner, the compiler, the dev server — running with nothing left to stop it.
	if cmd != nil {
		procutil.TerminateGroup(cmd)
	}
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
		cmd := s.cmd
		if s.cancel != nil {
			s.cancel()
		}
		path := s.mcpConfigPath
		s.mcpConfigPath = ""
		s.mu.Unlock()
		// Same reasoning as Stop: cancel reaches the agent, not the tree it forked.
		if cmd != nil {
			procutil.TerminateGroup(cmd)
		}
		if path != "" {
			_ = os.Remove(path) // the MCP config may hold credentials — don't leave it in /tmp
		}
	})
	return nil
}

// substitute expands {prompt}/{cwd} in the arg template. If no arg contains {prompt}, the prompt is
// appended as the final argument (so a bare command like `["exec"]` still receives it).
//
// {prompt} is expanded VERBATIM, which is correct only because the result is an argv element handed
// to exec — no shell parses it. A template whose expansion will be read BY a shell must use
// {prompt_sh} instead, which single-quotes. The remote-session template is exactly that case: ssh
// joins its trailing arguments and the remote LOGIN SHELL parses the result, so a verbatim {prompt}
// there made every prompt remote shell code (`; curl … | sh`) for anyone holding capSteer, and broke
// ordinary prompts besides — `fix the auth bug` word-split, and an apostrophe was a syntax error.
func substitute(tmpl []string, prompt, cwd, model, mcpConfig string) []string {
	out := make([]string, 0, len(tmpl)+1)
	sawPrompt := false
	for _, a := range tmpl {
		if strings.Contains(a, "{prompt}") || strings.Contains(a, "{prompt_sh}") {
			sawPrompt = true
		}
		a = strings.ReplaceAll(a, "{prompt_sh}", shellQuote(prompt))
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

// shellQuote single-quotes a string so a remote shell reads it as one literal word. Single quotes
// suppress every metacharacter, and the only byte that needs care is the quote itself: close, emit
// an escaped quote, reopen.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// HasContinuity implements agent.ContinuityReporter.
//
// False unless the config declares ResumeArgs: without them startTurn spawns a cold process for
// every turn (see the tmpl switch), so the agent has no memory of anything it said before. None of
// the ten built-in agents declares them today.
func (s *session) HasContinuity() bool { return len(s.cfg.ResumeArgs) > 0 }
