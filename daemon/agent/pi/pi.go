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

// Provider spawns pi RPC sessions.
type Provider struct {
	cmd []string // command to run pi in rpc mode, e.g. ["pi","--mode","rpc"]

	mu           sync.Mutex
	resume       map[string]string // our session id (pi_…) -> the JSONL session file pi opened for it
	resumeFile   string            // where the map persists across daemon restarts, 0600
	sessionsRoot string            // override for pi's sessions directory (tests / --session-dir)
}

// New returns a Provider that runs the given pi rpc command (argv). For tests, point
// it at a fake pi-rpc script speaking the JSONL protocol.
func New(cmd []string) *Provider {
	p := &Provider{cmd: cmd, resume: map[string]string{}}
	if home, err := os.UserHomeDir(); err == nil {
		p.resumeFile = filepath.Join(home, ".oculus", "pi-resume.json")
		if data, err := os.ReadFile(p.resumeFile); err == nil {
			_ = json.Unmarshal(data, &p.resume)
		}
	}
	return p
}

func (p *Provider) Name() string { return "pi" }

func (p *Provider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// Create starts a pi rpc session and sends the initial prompt.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	s, err := p.spawn(ctx, cwd, "pi_"+randID(), nil)
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
	id     string
	p      *Provider // for recording the session file pi opened (resume map)
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
	Type     string         `json:"type"`
	Command  string         `json:"command"` // set on {"type":"response"} frames — which command answered
	Method   string         `json:"method"`
	ID       string         `json:"id"`
	ToolName string         `json:"toolName"`
	Title    string         `json:"title"`
	Message  string         `json:"message"`
	Output   string         `json:"output"`
	Args     map[string]any `json:"args"`
	Asst     struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
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
		Usage struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cost   struct {
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
	for sc.Scan() {
		line := sc.Bytes()
		var e piEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "response":
			// get_state reports the session FILE pi is writing (docs/rpc.md). Record it: it's the only
			// handle that outlives this process, and therefore the only way a restore can resume this
			// exact conversation instead of starting a new one under the same id.
			s.recordResumeHandle(e.Command, line)
		case "message_end":
			// Per-turn token/cost usage (the "message" key here is an object → decode separately).
			var me piMessageEnd
			if json.Unmarshal(line, &me) == nil {
				u := me.Message.Usage
				if u.Input > 0 || u.Output > 0 || u.Cost.Total > 0 {
					s.emit(agent.Event{Type: protocol.TypeSessionUsage, Payload: protocol.SessionUsage{
						SessionID: s.id, InputTokens: u.Input, OutputTokens: u.Output, CostUSD: u.Cost.Total}})
				}
			}
		case "message_update":
			switch e.Asst.Type {
			case "text_delta":
				if e.Asst.Delta != "" {
					idle = false
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
				SessionID: s.id, ID: e.ID, Name: name, Title: title, Output: e.Output, Status: "completed"}})
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
				SessionID: s.id, ID: e.ID, Name: e.ToolName, Title: title, Status: "running"}})
		case "extension_ui_request":
			// confirm (yes/no) and select (options) both gate an action — a plan-mode
			// extension surfaces its plan through here, reusing the approval channel.
			if e.Method == "confirm" || e.Method == "select" {
				detail := e.Message
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
