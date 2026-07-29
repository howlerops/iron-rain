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
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Provider spawns pi RPC sessions.
type Provider struct {
	cmd []string // command to run pi in rpc mode, e.g. ["pi","--mode","rpc"]
}

// New returns a Provider that runs the given pi rpc command (argv). For tests, point
// it at a fake pi-rpc script speaking the JSONL protocol.
func New(cmd []string) *Provider { return &Provider{cmd: cmd} }

func (p *Provider) Name() string { return "pi" }

func (p *Provider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// Create starts a pi rpc session and sends the initial prompt.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	if len(p.cmd) == 0 {
		return nil, fmt.Errorf("pi: no command configured")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, p.cmd[0], p.cmd[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stderr = os.Stderr
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
		id:     "pi_" + randID(),
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
		close(s.events)
	}()
	if prompt != "" {
		_ = s.Prompt(ctx, prompt)
	}
	return s, nil
}

type session struct {
	id     string
	events chan agent.Event
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
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
	return s.send(map[string]any{"type": "prompt", "message": text})
}

// PromptImages sends a multimodal turn: pi takes an images array of {type,data,mimeType}
// (bare base64) alongside the message.
func (s *session) PromptImages(_ context.Context, text string, images []protocol.ImageAttachment) error {
	ims := make([]map[string]any, len(images))
	for i, im := range images {
		ims[i] = map[string]any{"type": "image", "data": im.Data, "mimeType": im.Mime}
	}
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
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

type piEvent struct {
	Type     string         `json:"type"`
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
			if len(v) > 200 {
				return v[:200] + "…"
			}
			return v
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

// readLoop drains pi's JSONL stdout and returns whether it observed a clean turn end (agent_end).
// It does NOT close s.events or emit a terminal backstop — the caller does that AFTER cmd.Wait(), so a
// non-zero exit can be surfaced as an error instead of being masked as a normal idle.
func (s *session) readLoop(stdout io.ReadCloser) (sawIdle bool) {
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
			// Rich tool card gets its output + completes (paired to the start by id).
			s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
				SessionID: s.id, ID: e.ID, Name: e.ToolName, Output: e.Output, Status: "completed"}})
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
				s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: e.ID, SessionID: s.id, Tool: tool, Detail: detail}})
			}
		case "agent_end":
			idle = true
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
