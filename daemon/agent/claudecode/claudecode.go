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
	return p.start(ctx, cwd, "cc_"+randID(), "create", prompt)
}

// Attach resumes an existing claude-code session by id (the sidecar uses --resume).
func (p *Provider) Attach(ctx context.Context, sessionID string) (agent.Session, error) {
	return p.start(ctx, "", sessionID, "attach", "")
}

func (p *Provider) start(ctx context.Context, cwd, id, mode, prompt string) (agent.Session, error) {
	if len(p.sidecar) == 0 {
		return nil, fmt.Errorf("claude-code: no sidecar configured")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, p.sidecar[0], p.sidecar[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), "OCULUS_SESSION_ID="+id, "OCULUS_MODE="+mode)
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
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go s.readLoop(stdout)
	if prompt != "" {
		_ = s.Prompt(ctx, prompt)
	}
	return s, nil
}

type session struct {
	id     string
	events chan agent.Event
	stdin  io.WriteCloser
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
	T       string `json:"t"`
	ID      string `json:"id,omitempty"`
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Message string `json:"message,omitempty"`
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
