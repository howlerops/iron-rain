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
	Args     map[string]any `json:"args"`
	Asst     struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

func (s *session) readLoop(stdout io.ReadCloser) {
	defer close(s.events)
	sc := bufio.NewScanner(stdout) // \n framing only — pi-rpc-compliant
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	idle := false
	for sc.Scan() {
		var e piEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
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
		case "tool_execution_start":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: "running " + e.ToolName}})
		case "extension_ui_request":
			if e.Method == "confirm" {
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
	if !idle {
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
	}
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
