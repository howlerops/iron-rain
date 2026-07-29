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
	"path/filepath"
	"strings"
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Provider spawns claude-code sessions via the sidecar.
type Provider struct {
	sidecar    []string // command to run the sidecar, e.g. ["node", "/path/sidecar.mjs"]
	mu         sync.Mutex
	resume     map[string]string // our stable session id (cc_…) -> claude's real session UUID
	resumePath string            // where the map persists (survives restart), 0600
}

// New returns a Provider that runs the given sidecar command (argv). For tests,
// point it at a fake sidecar script that speaks the stdio protocol.
func New(sidecar []string) *Provider {
	p := &Provider{sidecar: sidecar, resume: map[string]string{}}
	if home, err := os.UserHomeDir(); err == nil {
		p.resumePath = filepath.Join(home, ".oculus", "claude-resume.json")
		if data, err := os.ReadFile(p.resumePath); err == nil {
			_ = json.Unmarshal(data, &p.resume)
		}
	}
	return p
}

// resumeID returns claude's real session UUID for one of our session ids (empty if unknown).
func (p *Provider) resumeID(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resume[id]
}

// setResume records claude's real session UUID for our session id and persists it, so a resume
// after restart uses the UUID claude expects — not our cc_… id (which claude rejects as "not a UUID").
func (p *Provider) setResume(id, uuid string) {
	if id == "" || uuid == "" || id == uuid {
		return
	}
	p.mu.Lock()
	if p.resume[id] == uuid {
		p.mu.Unlock()
		return
	}
	p.resume[id] = uuid
	data, _ := json.MarshalIndent(p.resume, "", "  ")
	path := p.resumePath
	p.mu.Unlock()
	if path != "" && data != nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// CanResume implements agent.ResumeChecker: a claude-code session is only resumable when we know
// claude's real UUID for it (recorded on its first turn) or the id itself is that UUID. Without it,
// "re-attaching" starts a FRESH empty session that lies about being the old one.
func (p *Provider) CanResume(id string) bool {
	return p.resumeID(id) != "" || looksLikeUUID(id)
}

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
	sess, err := p.start(ctx, cwd, sessionID, "attach", "", false)
	if err != nil {
		return nil, err
	}
	// The Agent SDK resume does NOT re-emit past messages, so without this the pane is EMPTY after
	// attaching — for a discovered take-over AND for the daemon's own cc_ sessions restored after a
	// restart (their uuid comes from the resume map). Replay the tail of the on-disk transcript
	// (~/.claude/projects/…/<uuid>.jsonl) as history so the conversation shows immediately.
	if cs, ok := sess.(*session); ok && cs.replayUUID != "" {
		go cs.replayTranscript(cs.replayUUID)
	}
	return sess, nil
}

// SelfReplaying implements agent.Replayer: true only when this attach can actually replay its JSONL
// transcript (uuid known). When it can't, the hub's durable transcript is the history source —
// claiming self-replay unconditionally left restored cc_ sessions with NO history at all.
func (s *session) SelfReplaying() bool { return s.replayUUID != "" }

// looksLikeUUID reports whether id has the 8-4-4-4-12 hex shape of a claude session UUID.
func looksLikeUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// replayTranscriptMax bounds how many trailing messages a take-over replays (and how large any one
// message may be) so attaching to a huge session stays fast and the transcript stays renderable.
const (
	replayTranscriptMax     = 200
	replayTranscriptMsgSize = 20000
)

// replayTranscript reads the session's on-disk JSONL transcript and emits its trailing user/assistant
// messages as SessionMessage events (MsgID = the line's uuid, so the durable transcript dedups a
// re-attach). Best-effort: any parse hiccup just ends the replay.
func (s *session) replayTranscript(uuid string) {
	matches, _ := filepath.Glob(filepath.Join(discovery.DefaultClaudeProjectsDir(), "*", uuid+".jsonl"))
	if len(matches) == 0 {
		return
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return
	}
	defer f.Close()
	type msg struct{ role, text, id string }
	var tail []msg
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			var entry struct {
				Type    string `json:"type"`
				UUID    string `json:"uuid"`
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && (entry.Type == "user" || entry.Type == "assistant") {
				if text := transcriptText(entry.Message.Content); text != "" {
					if len(text) > replayTranscriptMsgSize {
						text = text[:replayTranscriptMsgSize] + "\n… [truncated]"
					}
					tail = append(tail, msg{role: entry.Message.Role, text: text, id: entry.UUID})
					if len(tail) > replayTranscriptMax {
						tail = tail[1:]
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	for _, m := range tail {
		role := m.role
		if role == "" {
			role = "assistant"
		}
		s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
			SessionID: s.id, Role: role, Text: m.text, MsgID: m.id,
		}})
	}
}

// transcriptText extracts the human-readable text from a transcript message's content: a plain
// string, or the concatenated text blocks of a content array (tool_use/tool_result blocks skipped).
func transcriptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.TrimSpace(str)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var out strings.Builder
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(b.Text)
		}
	}
	return strings.TrimSpace(out.String())
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
	// Resume with claude's real session UUID (captured on create), not our cc_… id which it rejects.
	replayUUID := ""
	if mode == "attach" {
		rid := p.resumeID(id)
		if rid == "" && looksLikeUUID(id) {
			// A DISCOVERED session (take-over): its id came from the on-disk transcript filename and
			// IS claude's real session UUID — resume it directly. Without this, take-over silently
			// started a FRESH sidecar session with none of the conversation.
			rid = id
		}
		if rid != "" {
			cmd.Env = append(cmd.Env, "OCULUS_CLAUDE_RESUME="+rid)
			replayUUID = rid // the JSONL transcript lives under claude's REAL uuid, whatever our id is
		}
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
		id:         id,
		replayUUID: replayUUID,
		p:          p,
		events:     make(chan agent.Event, 32),
		stdin:      stdin,
		cmd:        cmd,
		cancel:     cancel,
		done:       make(chan struct{}),
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
	p      *Provider // for recording claude's real session UUID (resume map)
	events chan agent.Event
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}

	// events now has TWO senders (readLoop + the take-over transcript replay), so closing it must be
	// mutually exclusive with sends: sendMu + closed make "send on closed channel" impossible.
	sendMu sync.Mutex
	closed bool

	// replayUUID is claude's real session uuid when this attach can replay its JSONL transcript
	// ("" = it can't — the hub's durable transcript is then the history source, see SelfReplaying).
	replayUUID string
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
	Output       string        `json:"output,omitempty"`
	Status       string        `json:"status,omitempty"`
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

// closeEvents ends the event stream exactly once, excluding concurrent emitters — the readLoop is no
// longer the only sender (replayTranscript emits from its own goroutine), so a bare close(s.events)
// could panic a concurrent send and take the whole daemon down (it did).
func (s *session) closeEvents() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

func (s *session) readLoop(stdout io.ReadCloser) {
	defer s.closeEvents()
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
			// The sidecar echoes our id, then reports claude's REAL session UUID on init. Record
			// the UUID so a later --resume uses it (claude rejects our cc_… id as "not a UUID").
			if s.p != nil {
				s.p.setResume(s.id, m.ID)
			}
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
		case "toolcall":
			// Rich inline tool card: running carries the command (Detail), the later result carries
			// Output. Same event the app renders for opencode, so claude-code gets card parity.
			if m.Status != "" && m.Status != "running" {
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: ""}})
			}
			s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
				SessionID: s.id, ID: m.ID, Name: m.Tool, Title: m.Detail, Output: m.Output, Status: m.Status,
			}})
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
