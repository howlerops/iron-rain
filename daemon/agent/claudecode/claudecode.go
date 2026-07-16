// Package claudecode implements agent.Provider against claude-code, run headless
// with stream-json output. Tool approvals use a PreToolUse hook that calls back to
// a per-session HTTP endpoint the provider hosts; the hook blocks until the app
// responds, then returns the decision to claude-code.
//
// NOTE: the exact claude-code settings/hook schema and stream-json event shapes
// should be verified against the installed claude-code; the parsing here targets
// the documented `{"type":"assistant","message":{"content":[{"type":"text","text"}]}}`
// and `{"type":"result"}` events and the PreToolUse `permissionDecision` contract.
package claudecode

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Provider spawns claude-code sessions.
type Provider struct {
	binary string
}

// New returns a Provider using the given claude binary (path or name on PATH).
func New(binary string) *Provider { return &Provider{binary: binary} }

func (p *Provider) Name() string { return "claude-code" }

// List returns no sessions (claude-code has no persistent server list in v0).
func (p *Provider) List(_ context.Context) ([]protocol.Session, error) { return nil, nil }

// Create spawns a claude-code run for the prompt, wiring approvals via a hook.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	s := &session{
		p:       p,
		id:      "cc_" + randID(),
		events:  make(chan agent.Event, 32),
		done:    make(chan struct{}),
		pending: map[string]chan string{},
	}
	if err := s.startApprovalServer(); err != nil {
		return nil, err
	}
	if err := s.spawn(ctx, cwd, prompt); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

type session struct {
	p      *Provider
	id     string
	events chan agent.Event
	done   chan struct{}

	ln      net.Listener
	httpSrv *http.Server
	approve string // approval endpoint URL

	cmd    *exec.Cmd
	cancel context.CancelFunc

	mu      sync.Mutex
	pending map[string]chan string

	closeOnce sync.Once
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "claude-code" }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) startApprovalServer() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.approve = "http://" + ln.Addr().String() + "/approve"

	mux := http.NewServeMux()
	mux.HandleFunc("/approve", s.handleApprove)
	s.httpSrv = &http.Server{Handler: mux}
	go func() { _ = s.httpSrv.Serve(ln) }()
	return nil
}

// handleApprove receives a tool from the claude PreToolUse hook, surfaces an
// approval request, blocks for the app's decision, and returns the hook JSON.
func (s *session) handleApprove(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var t struct {
		ToolName string `json:"tool_name"`
	}
	_ = json.Unmarshal(body, &t)

	approvalID := randID()
	ch := make(chan string, 1)
	s.mu.Lock()
	s.pending[approvalID] = ch
	s.mu.Unlock()

	s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
	s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{
		ApprovalID: approvalID, SessionID: s.id, Tool: t.ToolName, Input: json.RawMessage(body),
	}})

	decision := protocol.DecisionDeny
	select {
	case decision = <-ch:
	case <-s.done:
	}

	pd := "deny"
	if decision == protocol.DecisionAllow {
		pd = "allow"
	}
	resp := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":      "PreToolUse",
			"permissionDecision": pd,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *session) spawn(ctx context.Context, cwd, prompt string) error {
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	// Settings with the PreToolUse hook (for real claude-code). The fake test uses
	// $OCULUS_APPROVE_URL directly; real claude uses the hook command below.
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": "curl -s -X POST --data-binary @- " + s.approve,
				}},
			}},
		},
	}
	settingsPath := filepath.Join(os.TempDir(), "oculus-claude-"+s.id+".json")
	if b, err := json.Marshal(settings); err == nil {
		_ = os.WriteFile(settingsPath, b, 0o600)
	}

	cmd := exec.CommandContext(runCtx, s.p.binary, "-p", prompt, "--output-format", "stream-json", "--settings", settingsPath)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), "OCULUS_APPROVE_URL="+s.approve)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	s.cmd = cmd
	go s.readStream(stdout, settingsPath)
	return nil
}

func (s *session) readStream(stdout io.ReadCloser, settingsPath string) {
	defer close(s.events)
	defer os.Remove(settingsPath)

	idle := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "assistant":
			for _, c := range msg.Message.Content {
				if c.Type == "text" && c.Text != "" {
					s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: c.Text}})
				}
			}
		case "result":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
			idle = true
		}
	}
	if !idle {
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
	}
}

func (s *session) emit(ev agent.Event) {
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// Prompt is not supported in v0 (claude-code runs are single-shot headless).
func (s *session) Prompt(_ context.Context, _ string) error {
	return fmt.Errorf("claude-code: follow-up prompts not supported in v0")
}

func (s *session) Respond(_ context.Context, approvalID, decision string) error {
	s.mu.Lock()
	ch := s.pending[approvalID]
	delete(s.pending, approvalID)
	s.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("claude-code: no pending approval %s", approvalID)
	}
	ch <- decision
	return nil
}

func (s *session) Stop(_ context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
		if s.httpSrv != nil {
			_ = s.httpSrv.Close()
		}
	})
	return nil
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
