// Package claudecode drives claude-code as a PERSISTENT streaming session through a
// small Node sidecar (see sidecar/) built on the Claude Agent SDK. The daemon and the
// sidecar speak a line-delimited JSON protocol over stdio:
//
//	daemon -> sidecar : {"t":"prompt","text":"..."}          send a user turn
//	                    {"t":"approval","id":"..","decision":"allow|deny"}  answer a tool approval
//	                    {"t":"stop"}                          interrupt the current turn
//	sidecar -> daemon : {"t":"session","id":".."}             the session id
//	                    {"t":"text","text":".."}              assistant answer delta
//	                    {"t":"thinking","text":".."}          reasoning delta
//	                    {"t":"tool","tool":"..","detail":".."} a tool started running
//	                    {"t":"approval","id":"..","tool":"..","detail":".."}  approval REQUIRED (blocks in the sidecar)
//	                    {"t":"idle"}                          turn finished
//	                    {"t":"error","message":".."}
//
// This replaces the old single-shot `claude -p` + PreToolUse-HTTP-hook design, whose
// hook does NOT block in -p mode (anthropics/claude-code#36071) — tools could run
// unapproved. In streaming mode the sidecar's canUseTool callback genuinely blocks the
// tool until the daemon answers, so approvals are enforced.
//
// See ../../skills/oculus-... and sidecar/README.md.
package claudecode

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
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Provider spawns claude-code sessions via the sidecar.
type Provider struct {
	sidecar []string // command to run the sidecar, e.g. ["node", "/path/sidecar.mjs"]
}

// New returns a Provider that runs the given sidecar command (argv). For tests,
// point it at a fake sidecar script that speaks the stdio protocol.
func New(sidecar []string) *Provider { return &Provider{sidecar: sidecar} }

func (p *Provider) Name() string { return "claude-code" }

// List returns no live sessions (claude-code sessions are the daemon's child
// processes; discovery of on-disk transcripts is handled by daemon/discovery).
func (p *Provider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// Create starts a new streaming claude-code session and kicks it off with prompt.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.start(ctx, cwd, "cc_"+randID(), "create", prompt, false)
}

// CreatePlan starts a session in plan mode: the agent proposes a plan and requests approval
// (via ExitPlanMode → the normal approval channel) before making changes.
func (p *Provider) CreatePlan(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.start(ctx, cwd, "cc_"+randID(), "create", prompt, true)
}

// Models offers the Claude model aliases the Agent SDK accepts. Aliases resolve to the latest of
// each tier, so they stay valid across model refreshes without hardcoding dated ids.
func (p *Provider) Models(ctx context.Context) ([]protocol.ModelInfo, error) {
	return []protocol.ModelInfo{
		{ID: "opus", Name: "Claude Opus"},
		{ID: "sonnet", Name: "Claude Sonnet"},
		{ID: "haiku", Name: "Claude Haiku"},
	}, nil
}

// Attach resumes an existing claude-code session by id, running in its original cwd so the
// resumed session's tool calls (edits, bash) target the right project (the SDK's resume runs
// as a fresh process in the given directory, not the session's recorded one).
func (p *Provider) Attach(ctx context.Context, sessionID, cwd string) (agent.Session, error) {
	return p.start(ctx, cwd, sessionID, "attach", "", false)
}

func (p *Provider) start(ctx context.Context, cwd, id, mode, prompt string, plan bool) (agent.Session, error) {
	if len(p.sidecar) == 0 {
		return nil, fmt.Errorf("claude-code: no sidecar configured")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, p.sidecar[0], p.sidecar[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), "OCULUS_SESSION_ID="+id, "OCULUS_MODE="+mode)
	if plan {
		cmd.Env = append(cmd.Env, "OCULUS_PLAN=1")
	}
	cmd.Stderr = os.Stderr // surface sidecar errors
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
		events: make(chan agent.Event, 32),
		stdin:  stdin,
		cmd:    cmd,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	// readLoop returns on stdout EOF (which the ctx-cancel kill triggers); Wait() after
	// it returns reaps the child and releases the stdin/stdout pipe fds without racing
	// the scanner. Without this the process lingers as a zombie and its fds/goroutine leak.
	go func() {
		s.readLoop(stdout)
		_ = cmd.Wait()
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
func (s *session) Provider() string           { return "claude-code" }
func (s *session) Events() <-chan agent.Event { return s.events }

// inMsg / outMsg are the stdio protocol frames.
type inMsg struct {
	T        string   `json:"t"`
	Text     string   `json:"text,omitempty"`
	ID       string   `json:"id,omitempty"`
	Decision string   `json:"decision,omitempty"`
	Images   []imgAtt `json:"images,omitempty"`
}
type imgAtt struct {
	Mime string `json:"mime"`
	Data string `json:"data"`
}
type outMsg struct {
	T            string        `json:"t"`
	ID           string        `json:"id,omitempty"`
	Text         string        `json:"text,omitempty"`
	Tool         string        `json:"tool,omitempty"`
	Detail       string        `json:"detail,omitempty"`
	Message      string        `json:"message,omitempty"`
	InputTokens  int           `json:"input_tokens,omitempty"`
	OutputTokens int           `json:"output_tokens,omitempty"`
	CostUSD      float64       `json:"cost_usd,omitempty"`
	Todos        []sidecarTodo `json:"todos,omitempty"`
}

type sidecarTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func (s *session) send(m inMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(b)
	return err
}

// Prompt sends a follow-up user turn into the SAME running session.
func (s *session) Prompt(_ context.Context, text string) error {
	return s.send(inMsg{T: "prompt", Text: text})
}

// PromptImages sends a multimodal turn; the sidecar builds Anthropic image content blocks.
func (s *session) PromptImages(_ context.Context, text string, images []protocol.ImageAttachment) error {
	ims := make([]imgAtt, len(images))
	for i, im := range images {
		ims[i] = imgAtt{Mime: im.Mime, Data: im.Data}
	}
	return s.send(inMsg{T: "prompt", Text: text, Images: ims})
}

// Respond answers a tool approval; the sidecar's canUseTool unblocks. allow/always→allow.
func (s *session) Respond(_ context.Context, approvalID, decision string) error {
	d := "deny"
	if decision == protocol.DecisionAllow || decision == protocol.DecisionAlways {
		d = "allow"
	}
	return s.send(inMsg{T: "approval", ID: approvalID, Decision: d})
}

func (s *session) Stop(_ context.Context) error { return s.send(inMsg{T: "stop"}) }

// SetModel switches the model via the SDK's setModel (provider is unused — Claude ids stand alone).
func (s *session) SetModel(_, model string) error { return s.send(inMsg{T: "model", Text: model}) }

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		// Close stdin first so the sidecar sees EOF and can shut down gracefully; the
		// ctx-cancel below is the backstop that force-kills it. Either way readLoop
		// drains stdout to EOF and the reaping goroutine then Wait()s the child.
		if s.stdin != nil {
			s.writeMu.Lock()
			_ = s.stdin.Close()
			s.writeMu.Unlock()
		}
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

func (s *session) readLoop(stdout io.ReadCloser) {
	defer close(s.events)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	idle := false
	for sc.Scan() {
		var m outMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.T {
		case "session":
			// id is generated by the daemon and passed in; nothing to do.
		case "text":
			if m.Text != "" {
				idle = false
				s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: m.Text}})
			}
		case "thinking":
			if m.Text != "" {
				idle = false
				s.emit(agent.Event{Type: protocol.TypeThinking, Payload: protocol.Thinking{SessionID: s.id, Text: m.Text}})
			}
		case "tool":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: "running " + m.Tool}})
		case "approval":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
			s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: m.ID, SessionID: s.id, Tool: m.Tool, Detail: m.Detail}})
		case "idle":
			idle = true
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
		case "error":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: m.Message}})
		case "usage":
			s.emit(agent.Event{Type: protocol.TypeSessionUsage, Payload: protocol.SessionUsage{
				SessionID: s.id, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CostUSD: m.CostUSD}})
		case "todos":
			todos := make([]protocol.Todo, len(m.Todos))
			for i, td := range m.Todos {
				todos[i] = protocol.Todo{Content: td.Content, Status: td.Status}
			}
			s.emit(agent.Event{Type: protocol.TypeSessionTodos, Payload: protocol.SessionTodos{SessionID: s.id, Todos: todos}})
		}
	}
	if !idle {
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
	}
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
