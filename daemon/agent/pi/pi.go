// Package pi drives the pi coding agent (pi.dev / @earendil-works/pi-coding-agent)
// through its RPC mode (`pi --mode rpc`): a JSONL protocol over stdin/stdout. The
// daemon owns the one `pi` child process (a single stdio pipe → the daemon is the
// only possible reader, so it MUST be the fan-out point — exactly the hub's
// single-session-broadcast model).
//
// Protocol (see pi's docs/rpc.md; verified against pi 0.80.2):
//
//	daemon -> pi : {"type":"prompt","message":"..."}                 a user turn
//	               {"type":"extension_ui_response","id","confirmed":bool}  answer a confirm() approval
//	               {"type":"abort"}                                   interrupt
//	pi -> daemon : {"type":"message_update","assistantMessageEvent":{"type":"text_delta"|"thinking_delta","delta"}}
//	               {"type":"tool_execution_start","toolName","args"}  a tool started
//	               {"type":"extension_ui_request","id","method":"confirm","title","message"}  approval REQUIRED (blocks in pi)
//	               {"type":"agent_end"}                               the run finished
//
// STATUS: spike — enabled with --pi and unit-tested against a fake pi-rpc; the exact
// confirm-request fields + which tools trigger a confirm depend on pi's config/
// extensions, so validate live before relying on it. Approvals map allow/always→confirmed.
package pi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/procutil"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/textutil"
)

// maxLoggedBadFrames caps the unparseable-frame log per session.
const maxLoggedBadFrames = 5

// Provider spawns pi RPC sessions.
type Provider struct {
	name string   // provider name shown in the UI; "" means pi
	cmd  []string // command to run pi in rpc mode, e.g. ["pi","--mode","rpc"]

	mu           sync.Mutex
	resume       map[string]string // our session id (pi_…) -> the JSONL session file pi opened for it
	resumeFile   string            // where the map persists across daemon restarts, 0600
	sessionsRoot string            // override for pi's sessions directory (tests / --session-dir)
}

// New returns a Provider that runs the given pi rpc command (argv). For tests, point
// it at a fake pi-rpc script speaking the JSONL protocol.
func New(cmd []string) *Provider { return NewNamed("pi", cmd, DefaultSessionsRoot()) }

// NewNamed returns a Provider under a different name and session root, for another agent that
// speaks the SAME JSONL RPC protocol.
//
// prime-agent (Prime Intellect) is the case this exists for. Its `--mode rpc` emits the identical
// event vocabulary — agent_start, turn_start, message_update with an assistantMessageEvent
// text_delta, message_end carrying usage, agent_end — and accepts the same
// {"type":"prompt","message":...} on stdin. Verified by driving a real prime-agent through this
// adapter unchanged. Reimplementing it would have meant a second copy of a protocol we already
// parse, and the issue-inspector markdown renderer is a standing reminder of what a second copy
// costs: it drifts, and only one of them gets fixed.
//
// The two differ in exactly two mechanical ways, both parameters here: the provider name, and where
// sessions live (~/.pi/agent/sessions nested per project, ~/.prime/agent/sessions flat).
func NewNamed(name string, cmd []string, sessionsRoot string) *Provider {
	p := &Provider{name: name, cmd: cmd, resume: map[string]string{}, sessionsRoot: sessionsRoot}
	if home, err := os.UserHomeDir(); err == nil {
		p.resumeFile = filepath.Join(home, ".oculus", name+"-resume.json")
		if data, err := os.ReadFile(p.resumeFile); err == nil {
			_ = json.Unmarshal(data, &p.resume)
		}
	}
	return p
}

func (p *Provider) Name() string {
	if p.name == "" {
		return "pi"
	}
	return p.name
}

func (p *Provider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// Create starts a pi rpc session and sends the initial prompt.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	// Prefix by PROVIDER, not a hardcoded "pi_". prime-agent shares this adapter, so its sessions
	// were being minted as pi_… — the id is user-visible (it is the pill at the bottom of the
	// session view and what appears in logs), so a prime-agent session claiming to be pi is a
	// straightforward lie about which agent is running.
	s, err := p.spawn(ctx, cwd, p.Name()+"_"+randID(), nil)
	if err != nil {
		return nil, err
	}
	// Ask pi which session file it opened. That path is the ONLY handle that survives this process,
	// so it's what a later restore resumes from — pi names files by its own uuid, which our id isn't.
	_ = s.send(map[string]any{"type": "get_state"})
	if prompt != "" {
		_ = s.Prompt(ctx, prompt)
	}
	return s, nil
}

// spawn starts one pi rpc child and wires its lifecycle. extraArgs is appended to the configured
// command (Attach adds `--session <file>` to resume an existing conversation).
func (p *Provider) spawn(_ context.Context, cwd, id string, extraArgs []string) (*session, error) {
	if len(p.cmd) == 0 {
		return nil, fmt.Errorf("pi: no command configured")
	}
	argv := append(append([]string{}, p.cmd[1:]...), extraArgs...)
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, p.cmd[0], argv...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stderr = procutil.LogWriter("pi") // into the daemon log/loghub, not the raw inherited FD
	procutil.Isolate(cmd)                 // pi can shell out — terminate the whole tree
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	s := &session{
		id:     id,
		p:      p,
		cwd:    cwd,
		rpc:    newRPCWaiters(),
		events: make(chan agent.Event, 32),
		stdin:  stdin,
		cmd:    cmd,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	// readLoop returns on stdout EOF (which the ctx-cancel kill triggers); Wait() after
	// it returns reaps the child and releases the stdin/stdout pipe fds. Without this the
	// process lingers as a zombie and its fds/CommandContext watcher goroutine leak.
	go func() {
		sawIdle := s.readLoop(stdout)
		err := cmd.Wait()
		// Terminal status. A clean agent_end already emitted idle. Otherwise the stream ended without
		// one: a non-zero exit we didn't initiate is a CRASH — surface it as an error so it isn't
		// masked as a normal "Finished"; anything else is a plain idle backstop so the app unsticks.
		shuttingDown := false
		select {
		case <-s.done:
			shuttingDown = true // Close()/Stop() killed it — not a crash
		default:
		}
		if !sawIdle && !shuttingDown {
			if err != nil {
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
					SessionID: s.id, Status: protocol.StatusError, Detail: "pi exited: " + err.Error()}})
			} else {
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
					SessionID: s.id, Status: protocol.StatusIdle}})
			}
		}
		s.closeEvents()
	}()
	return s, nil
}

type session struct {
	id  string
	p   *Provider // for recording the session file pi opened (resume map)
	cwd string    // working directory, for the branch/cwd a status bar shows

	// RPC request/response (see thread.go). pi answers a command with
	// {"type":"response","command":…}; rpcMu serialises round trips because responses carry no id.
	rpcMu  sync.Mutex
	rpc    *rpcWaiters
	events chan agent.Event
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}

	// resumedPath is the on-disk session file this session was resumed from ("" for a fresh one);
	// it also marks the session as self-replaying (see SelfReplaying).
	resumedPath string

	// sawText is whether this turn has already streamed assistant text, so a following message_start
	// knows whether a paragraph break is needed. Only ever touched from the read loop.
	sawText bool

	// busy is whether a turn is in flight: set when we hand pi a prompt, cleared on its agent_end.
	// It is the answer to the hub reconciler's Probe, and it is tracked here rather than inferred
	// from event timing because a turn wedged inside a tool produces no events at all — the exact
	// case where guessing from silence gets it wrong.
	busy atomic.Bool

	// events now has TWO senders (readLoop and the resume transcript replay), so closing it must be
	// mutually exclusive with sends — a bare close() could panic a concurrent send and take the whole
	// daemon down. Same guard the claude adapter needed once it gained a replay goroutine.
	sendMu sync.Mutex
	closed bool

	// toolCards remembers a running tool card's identity until tool_execution_end arrives. pi splits
	// one card across two frames and only tool_execution_start carries the title (and reliably the
	// tool name); the end frame is the state the hub PERSISTS. Without re-attaching here the durable
	// row keeps the output but loses the command summary, so a card that read "bash · npm test"
	// while it ran comes back from history as an untitled box — the same defect the claude adapter
	// had, and just as invisible live, because the app merges the end frame onto the card already on
	// screen and keeps the old fields.
	toolMu    sync.Mutex
	toolCards map[string]toolCard
}

// toolCard is the identity half of an inline tool card, held only between tool_execution_start and
// its matching tool_execution_end.
type toolCard struct{ name, title string }

// rememberToolCard records a running card's identity. A frame carrying neither field stores
// nothing, so it can never overwrite a real identity with blanks.
func (s *session) rememberToolCard(id, name, title string) {
	if id == "" || (name == "" && title == "") {
		return
	}
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	if s.toolCards == nil {
		s.toolCards = map[string]toolCard{}
	}
	s.toolCards[id] = toolCard{name: name, title: title}
}

// takeToolCard returns and DELETES a remembered card. Delete-on-read bounds the map: the entry is
// dead the instant the end frame is emitted, and a session that stays up for days runs thousands of
// tools. A miss — an end frame with no prior start, as after a daemon restart mid-turn — returns
// the zero value and the fields stay empty; we never invent a name for a tool we did not see start.
func (s *session) takeToolCard(id string) toolCard {
	if id == "" {
		return toolCard{}
	}
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	c, ok := s.toolCards[id]
	if ok {
		delete(s.toolCards, id)
	}
	return c
}

// forgetToolCards drops every still-pending card at teardown. A tool interrupted by a stop or a
// crashed child never gets its end frame, so delete-on-read alone would strand those entries.
func (s *session) forgetToolCards() {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	s.toolCards = nil
}

// closeEvents ends the event stream exactly once, excluding concurrent emitters.
func (s *session) closeEvents() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "pi" }
func (s *session) Events() <-chan agent.Event { return s.events }

// Capabilities declares what pi can do (agent.Capable).
//
// pi is the provider with the richest THREAD model — /tree lists the branch points and you can fork
// from any earlier user message — which is exactly the sort of thing the old
// intersection-shaped adapter layer had no way to express, so it simply never reached the client.
//
// Efforts are pi's --thinking levels, in the order the CLI documents them.
// threadCapableAgents are the products on this adapter that actually ship the session-tree
// machinery, checked in their installed source rather than assumed from the shared protocol.
//
// The adapter drives more than pi: prime-agent (Prime Intellect) speaks the same JSONL RPC protocol
// and rides it unchanged (see autodetect.go). Sharing a WIRE protocol would not by itself mean
// sharing a feature — but these two share a codebase lineage, and prime-agent's install has
// navigateTree, the branch-summary collection, and the app.session.tree / app.session.fork /
// app.session.resume actions, same as pi's.
//
// An allowlist rather than "anything on this adapter", because NewNamed takes an arbitrary name: a
// third product could ride the same RPC tomorrow with none of this, and would inherit controls that
// fail when tapped — which is the one thing a capability manifest exists to prevent.
var threadCapableAgents = map[string]bool{"pi": true, "prime-agent": true}

func (s *session) Capabilities() protocol.SessionCapabilities {
	name := "pi"
	if s.p != nil {
		name = s.p.Name()
	}
	caps := protocol.SessionCapabilities{
		SessionID: s.id,
		Provider:  name,
		Modes:     protocol.Modes(),
		Efforts:   []string{"off", "minimal", "low", "medium", "high", "xhigh"},
		Commands:  true,
		Models:    true,
	}
	if threadCapableAgents[name] {
		// Declared against what the RPC MODE exposes, which is a strict subset of what the TUI does.
		//
		// The interactive TUI has the full picture: /tree calls navigateTree, moving the session's
		// leaf anywhere in a 19-node tree of messages, tool calls and edits, optionally summarising
		// the branch it leaves behind. None of that is reachable over `--mode rpc`. The commands that
		// exist are get_fork_messages (USER MESSAGES only — not the tree), fork(entryId), clone and
		// compact. There is no navigate_tree.
		//
		// So Rewind and Summarize are false. Claiming them because the product can do them in another
		// mode would put controls in the app that this adapter cannot honour, which is the exact
		// failure the manifest exists to prevent — and a "rewind" that silently forked instead, with
		// no branch summary, would be worse than an absent button.
		caps.Thread = protocol.ThreadCaps{Tree: true, Fork: true, Compact: true}
	}
	return caps
}

// Facts reports live ambient state (agent.Factual).
func (s *session) Facts(context.Context) protocol.SessionFacts {
	return protocol.SessionFacts{
		SessionID: s.id,
		CWD:       s.cwd,
		Branch:    agent.GitBranch(s.cwd),
	}
}

func (s *session) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(b)
	return err
}

// Prompt sends a user turn into the running pi session.
func (s *session) Prompt(_ context.Context, text string) error {
	s.busy.Store(true)
	// A new turn starts a fresh message chain, so its first message must not open with a break.
	s.sawText = false
	return s.send(map[string]any{"type": "prompt", "message": text})
}

// Probe implements agent.Prober: is a turn in flight, and is pi still there to run it?
//
// Without this the hub's reconciler skipped pi entirely — it only probes sessions implementing
// Prober — so a wedged pi turn heartbeated "working" forever and a pi process that had died left
// its session rendering as busy with nothing able to say otherwise.
func (s *session) Probe(context.Context) (bool, error) {
	select {
	case <-s.done:
		return false, errors.New("pi: session is closed")
	default:
	}
	// The process is the session here: if it is gone, "busy" is meaningless and the truthful answer
	// is unreachable, which is what lets the reconciler eventually declare the turn abandoned.
	if s.cmd != nil && s.cmd.ProcessState != nil && s.cmd.ProcessState.Exited() {
		return false, fmt.Errorf("pi: process exited (%s)", s.cmd.ProcessState)
	}
	return s.busy.Load(), nil
}

// PromptImages sends a multimodal turn: pi takes an images array of {type,data,mimeType}
// (bare base64) alongside the message.
func (s *session) PromptImages(_ context.Context, text string, images []protocol.ImageAttachment) error {
	ims := make([]map[string]any, len(images))
	for i, im := range images {
		ims[i] = map[string]any{"type": "image", "data": im.Data, "mimeType": im.Mime}
	}
	s.busy.Store(true)
	return s.send(map[string]any{"type": "prompt", "message": text, "images": ims})
}

// Respond answers a confirm() approval. allow/always→confirmed:true.
func (s *session) Respond(_ context.Context, approvalID, decision string) error {
	confirmed := decision == protocol.DecisionAllow || decision == protocol.DecisionAlways
	return s.send(map[string]any{"type": "extension_ui_response", "id": approvalID, "confirmed": confirmed})
}

func (s *session) Stop(_ context.Context) error {
	return s.send(map[string]any{"type": "abort"})
}

// Nudge implements agent.Nudger: append a user turn over stdin without aborting the running one.
// pi's protocol keeps "prompt" and "abort" as separate messages, so a prompt sent mid-turn can only
// be queued or consumed — never destructive. Delivery mid-turn is pi's call; that is exactly the
// best-effort the Nudger contract allows.
func (s *session) Nudge(_ context.Context, text string) error {
	return s.send(map[string]any{"type": "prompt", "message": text})
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
	})
	return nil
}

func (s *session) emit(ev agent.Event) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

type piEvent struct {
	Type     string `json:"type"`
	Command  string `json:"command"` // set on {"type":"response"} frames — which command answered
	Method   string `json:"method"`
	ID       string `json:"id"`
	ToolName string `json:"toolName"`
	Title    string `json:"title"`
	// RawMessage, NOT string. pi reuses the "message" key with two different types: a plain string on
	// a confirm request, but an OBJECT on message_start/message_update/message_end. Typed as a string
	// it failed to decode every object-carrying frame — and because readLoop skips any line that
	// fails to unmarshal, EVERY assistant text_delta was silently dropped. The visible result was an
	// agent that accepted prompts, ended its turns idle, and never said a word.
	//
	// json.RawMessage accepts either shape; messageText pulls the string back out where one is
	// actually expected.
	Message json.RawMessage `json:"message"`
	Output  string          `json:"output"`
	Args    map[string]any  `json:"args"`
	Asst    struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

// messageText returns the "message" field when it is a plain string (a confirm/select prompt), and
// "" when it is an object or absent. Callers want human-readable text; an object here is a different
// frame shape, not a message to show.
func (e piEvent) messageText() string {
	if len(e.Message) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Message, &s) != nil {
		return ""
	}
	return s
}

// piToolSummary renders a short command line from a tool's args (command → path → pattern), so a
// tool card reads "bash · npm test" instead of just the tool name.
func piToolSummary(args map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query"} {
		if v, ok := args[k].(string); ok && v != "" {
			return textutil.Trunc(v, 200)
		}
	}
	return ""
}

// piMessageEnd is decoded separately from a message_end line (its "message" key is an object,
// whereas piEvent.Message is a string for other events — same key, so keep them apart).
type piMessageEnd struct {
	Message struct {
		Role string `json:"role"`
		// A FAILED turn is reported here and nowhere else.
		//
		// The adapter read neither of these, so a turn that errored — a model without tool support,
		// a refused key, a 400 from the provider — emitted a plain idle and the app showed
		// "Finished" over an empty reply. Observed with oh-my-pi defaulting to an ollama model:
		//   stopReason: "error"
		//   errorMessage: "400 ... vicuna:latest does not support tools"
		// The user is told nothing, which is the same silent-failure shape this file's own header
		// warns about twice.
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
		Usage        struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			// Observed on the wire and previously unread, so pi's cache traffic — the bulk of a long
			// session — never reached the client:
			//   "usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,
			//            "cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}
			CacheRead  int `json:"cacheRead"`
			CacheWrite int `json:"cacheWrite"`
			Cost       struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

// piTodo tool arg names a coding agent might use for a todo/task list (pi has none natively;
// a valhalla-style extension provides one, and its call arrives as tool_execution_start).
func todosFromToolArgs(toolName string, args map[string]any) ([]protocol.Todo, bool) {
	switch strings.ToLower(toolName) {
	case "todowrite", "todo", "todos", "todo_write", "update_todos", "task", "tasks":
	default:
		return nil, false
	}
	raw, ok := args["todos"]
	if !ok {
		raw = args["items"]
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]protocol.Todo, 0, len(list))
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		content, _ := m["content"].(string)
		if content == "" {
			content, _ = m["text"].(string)
		}
		status, _ := m["status"].(string)
		if status == "" {
			status = "pending"
		}
		if content != "" {
			out = append(out, protocol.Todo{Content: content, Status: status})
		}
	}
	return out, len(out) > 0
}

// recordResumeHandle captures the session FILE pi is writing, reported in its get_state response.
//
// Extracted from readLoop so it can be tested at all. It is the ONLY handle that outlives this
// process: pi names its files `<timestamp>_<uuid>.jsonl`, so nothing can find one again from our
// `pi_…` id. If this stops matching pi's real frame shape, daemon-created pi sessions become
// silently unresumable — a failure with no error anywhere, which is why it needs coverage rather
// than trust.
func (s *session) recordResumeHandle(command string, line []byte) {
	if command != "get_state" || s.p == nil {
		return
	}
	var st struct {
		Data struct {
			SessionFile string `json:"sessionFile"`
		} `json:"data"`
	}
	if json.Unmarshal(line, &st) != nil || st.Data.SessionFile == "" {
		return // a payload with no file tells us nothing; recording "" would erase a good handle
	}
	s.p.setResume(s.id, st.Data.SessionFile)
}

// readLoop drains pi's JSONL stdout and returns whether it observed a clean turn end (agent_end).
// It does NOT close s.events or emit a terminal backstop — the caller does that AFTER cmd.Wait(), so a
// non-zero exit can be surfaced as an error instead of being masked as a normal idle.
func (s *session) readLoop(stdout io.ReadCloser) (sawIdle bool) {
	// stdout EOF means the child is gone (normal exit, stop, kill, crash), so this is the one
	// teardown path every session takes — release any card that never got its end frame here.
	defer s.forgetToolCards()
	sc := bufio.NewScanner(stdout) // \n framing only — pi-rpc-compliant
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	idle := false
	badFrames := 0
	for sc.Scan() {
		line := sc.Bytes()
		var e piEvent
		if err := json.Unmarshal(line, &e); err != nil {
			// Log it. This skip is correct — one malformed frame must not kill the stream — but doing
			// it SILENTLY is how a decode bug hid in plain sight: piEvent.Message was typed string
			// while pi sends an object on message_* frames, so every assistant text_delta failed to
			// parse and was dropped here without a trace. The agent looked alive and mute.
			// Rate-limited to the first few per session so a systematically bad frame is visible
			// without flooding the log.
			if badFrames < maxLoggedBadFrames {
				badFrames++
				log.Printf("pi: dropping an unparseable frame: %v (%s)", err, textutil.FirstLine(string(line), 160))
			}
			continue
		}
		switch e.Type {
		case "response":
			// get_state reports the session FILE pi is writing (docs/rpc.md). Record it: it's the only
			// handle that outlives this process, and therefore the only way a restore can resume this
			// exact conversation instead of starting a new one under the same id.
			s.recordResumeHandle(e.Command, line)
			// Hand the frame to whoever asked (thread.go's request/response calls), keyed by the id
			// pi echoes back. Unconditional and after the above: a response nobody is waiting for
			// resolves nothing and costs nothing, and get_state is sent fire-and-forget at startup
			// with no id at all, so it can never be mistaken for someone's awaited reply.
			s.rpc.resolve(e.ID, line)
		case "message_end":
			// Per-turn token/cost usage (the "message" key here is an object → decode separately).
			var me piMessageEnd
			if json.Unmarshal(line, &me) == nil {
				u := me.Message.Usage
				if u.Input > 0 || u.Output > 0 || u.Cost.Total > 0 || u.CacheRead > 0 || u.CacheWrite > 0 {
					s.emit(agent.Event{Type: protocol.TypeSessionUsage, Payload: protocol.SessionUsage{
						SessionID:        s.id,
						InputTokens:      u.Input,
						OutputTokens:     u.Output,
						CacheReadTokens:  u.CacheRead,
						CacheWriteTokens: u.CacheWrite,
						CostUSD:          u.Cost.Total,
						CostReported:     u.Cost.Total > 0,
					}})
				}
			}
		case "message_start":
			// A NEW assistant message inside the same turn.
			//
			// pi sends several of these per turn — measured at 18 of 204 turns across real sessions
			// here, up to eight in one turn. The client buffers deltas into one bubble until the turn
			// ends, so without a separator the last sentence of one message and the first word of the
			// next are glued into a single word and the whole reply collapses into one run-on
			// paragraph. Same defect opencode had; different frame to hang the boundary off.
			if messageRole(e.Message) == "assistant" && s.sawText {
				s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: "\n\n"}})
				s.sawText = false
			}
		case "message_update":
			switch e.Asst.Type {
			case "text_delta":
				if e.Asst.Delta != "" {
					idle = false
					s.sawText = true
					s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: e.Asst.Delta}})
				}
			case "thinking_delta":
				if e.Asst.Delta != "" {
					idle = false
					s.emit(agent.Event{Type: protocol.TypeThinking, Payload: protocol.Thinking{SessionID: s.id, Text: e.Asst.Delta}})
				}
			}
		case "tool_execution_end":
			// Rich tool card gets its output + completes (paired to the start by id). Re-attach the
			// name/title cached at start: this is the state the hub makes durable, and the end frame
			// carries no title at all (and not always a toolName), so emitting it as-is is what
			// silently strips the card's command summary from history.
			name, title := e.ToolName, e.Title
			c := s.takeToolCard(e.ID)
			if name == "" {
				name = c.name
			}
			if title == "" {
				title = c.title
			}
			s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
				SessionID: s.id, ID: e.ID, Name: name, Title: title, Output: e.Output, Status: protocol.ToolCompleted}})
		case "tool_execution_start":
			// pi has no native to-do tool; a valhalla-style extension can add one, and its
			// call arrives here — surface it as the normalized session.todos.
			if todos, ok := todosFromToolArgs(e.ToolName, e.Args); ok {
				s.emit(agent.Event{Type: protocol.TypeSessionTodos, Payload: protocol.SessionTodos{SessionID: s.id, Todos: todos}})
			}
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: "running " + e.ToolName}})
			// Rich inline tool card (running) with a command summary; completed by tool_execution_end.
			title := e.Title
			if title == "" {
				title = piToolSummary(e.Args)
			}
			s.rememberToolCard(e.ID, e.ToolName, title)
			s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
				SessionID: s.id, ID: e.ID, Name: e.ToolName, Title: title, Status: protocol.ToolRunning}})
		case "extension_ui_request":
			// EVERY request must be answered, not just the ones that gate an action. pi blocks the
			// run until it receives an extension_ui_response for each request it sends, and its
			// extensions open a turn by registering UI surfaces — setWidget ("autoresearch", "goal",
			// "subagent-async") and setStatus. Those are not questions for the user, but ignoring
			// them wedged the agent before it produced a single token: the prompt was accepted
			// ({"type":"response","command":"prompt","success":true}) and then nothing ever arrived,
			// so the turn closed idle with an empty reply and the session looked dead.
			//
			// Acknowledged immediately here. Only confirm/select are real questions and reach the
			// user; a non-gating request answered by a human would be a prompt about nothing.
			if e.Method != "confirm" && e.Method != "select" {
				if err := s.send(map[string]any{"type": "extension_ui_response", "id": e.ID, "confirmed": true}); err != nil {
					log.Printf("pi: failed to ack %s ui request: %v", e.Method, err)
				}
				continue
			}
			// confirm (yes/no) and select (options) both gate an action — a plan-mode
			// extension surfaces its plan through here, reusing the approval channel.
			{
				detail := e.messageText()
				if detail == "" {
					detail = e.Title
				}
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
				tool := e.ToolName
				if tool == "" {
					tool = "confirm"
				}
				// Forward the tool args so an "always allow" can be scoped by path/command, not just by tool.
				var rawArgs json.RawMessage
				if len(e.Args) > 0 {
					if b, err := json.Marshal(e.Args); err == nil {
						rawArgs = b
					}
				}
				s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: e.ID, SessionID: s.id, Tool: tool, Detail: detail, Input: rawArgs}})
			}
		case "agent_end":
			idle = true
			s.busy.Store(false)
			// A turn that ended in an error must not close as a plain idle: the reply is empty, and
			// without this the user is shown a finished turn with nothing in it and no reason.
			if msg, ok := failedTurn(line); ok {
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
					SessionID: s.id, Status: protocol.StatusError, Detail: msg}})
				continue
			}
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
		}
	}
	return idle
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// failedTurn reports the error an agent_end carries, if the turn failed.
//
// agent_end repeats the whole conversation, so the ASSISTANT message is what matters and it is the
// last one. A turn can end with stopReason "error" and a populated errorMessage while every other
// frame in the stream looks perfectly normal — which is exactly how a broken model configuration
// presented as a successful, empty turn.
func failedTurn(line []byte) (string, bool) {
	var end struct {
		Messages []struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"messages"`
	}
	if json.Unmarshal(line, &end) != nil {
		return "", false
	}
	for i := len(end.Messages) - 1; i >= 0; i-- {
		m := end.Messages[i]
		if m.Role != "assistant" {
			continue
		}
		if m.StopReason != "error" && m.ErrorMessage == "" {
			return "", false
		}
		if msg := textutil.FirstLine(strings.TrimSpace(m.ErrorMessage), 300); msg != "" {
			return msg, true
		}
		return "the agent's turn ended in an error it did not describe", true
	}
	return "", false
}

// messageRole reads just the role out of a message_start/message_end frame's `message` object.
//
// Decoded on demand rather than as a struct field because pi reuses the `message` key for objects of
// different shapes across frame types — the reason piEvent.Message is a RawMessage at all.
func messageRole(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.Role
}
