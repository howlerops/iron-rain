// Package agui adapts an AG-UI-speaking agent backend into an Iron Rain provider.
//
// WHY THIS EXISTS. The generic `cli` adapter is the only way to bring your own agent today, and it
// carries almost nothing: status and output deltas, with no tool calls, no approvals, no usage, no
// sub-agents. Anything richer has meant writing a bespoke Go adapter per harness. AG-UI is an open,
// widely-adopted event protocol that already models the things `cli` is missing, so speaking it gives
// custom agents a genuinely capable integration path for one adapter's worth of work.
//
// WHY WE IMPLEMENT THE WIRE FORMAT OURSELVES rather than importing the official SDK. The Go SDK is
// community-tier: it publishes no semver tags (consumers pin a pseudo-version against a monorepo
// commit), it lags the TypeScript/Python SDKs by weeks, its BaseEvent omits the `metadata` field
// every other SDK carries, six of its event types validate but fail to decode, its errors are
// untyped strings the reference server matches with strings.Contains, and it has an open
// unbounded-buffer bug. The format it would save us is `data: <json>\n\n` plus a set of structs.
// That is not a dependency worth taking for a protocol with no version field.
//
// THE IMPEDANCE MISMATCH, and how it is resolved. AG-UI is a RUN protocol: one HTTP POST carries one
// run, and the stream ends when the run does. Iron Rain sessions are long-lived and resumable. The
// two reconcile cleanly because AG-UI already separates the two ideas — a `threadId` spans runs, a
// `runId` identifies one. So an Iron Rain session IS an AG-UI thread, and every Prompt opens a new
// run against it. Nothing has to be faked.
//
// LIVENESS. AG-UI models no heartbeat, no stall, no recovery — nothing the turn engine consumes.
// That would normally make a provider invisible to stall detection. It is recoverable here because
// the TRANSPORT supplies what the protocol omits: a run is in flight exactly while its POST is
// unfinished, which is authoritative in the same way opencode's Prober is. See Probe.
package agui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// AG-UI event type names. Only the ones we translate are listed; anything else is ignored rather
// than treated as an error, because the protocol carries no version field and evolves additively —
// an unknown name is far more likely to be a newer event than a broken one.
const (
	evRunStarted  = "RUN_STARTED"
	evRunFinished = "RUN_FINISHED"
	evRunError    = "RUN_ERROR"

	evTextStart   = "TEXT_MESSAGE_START"
	evTextContent = "TEXT_MESSAGE_CONTENT"
	evTextEnd     = "TEXT_MESSAGE_END"
	evTextChunk   = "TEXT_MESSAGE_CHUNK"

	evToolStart  = "TOOL_CALL_START"
	evToolArgs   = "TOOL_CALL_ARGS"
	evToolEnd    = "TOOL_CALL_END"
	evToolResult = "TOOL_CALL_RESULT"

	evReasoningContent = "REASONING_MESSAGE_CONTENT"
	// Superseded by REASONING_* upstream but still emitted by older integrations, and free to accept.
	evThinkingContent = "THINKING_TEXT_MESSAGE_CONTENT"
)

// runTimeout bounds a single run. Generous because an agent may legitimately work for a long time;
// the turn engine's stall detection is what handles "alive but not progressing", not this.
const runTimeout = 6 * time.Hour

// maxLineBytes caps one SSE line. AG-UI encodes each event as single-line JSON, so a line larger
// than this is a runaway producer rather than a real event.
const maxLineBytes = 8 << 20

// Config describes one AG-UI backend.
type Config struct {
	Name     string            `json:"name"`     // provider name shown in the UI
	Endpoint string            `json:"endpoint"` // absolute URL that accepts a RunAgentInput POST
	Headers  map[string]string `json:"headers,omitempty"`
}

// Provider creates sessions against one AG-UI endpoint.
type Provider struct {
	cfg  Config
	http *http.Client
}

// New returns a provider for cfg.
//
// The http.Client deliberately has NO Timeout: it would bound the whole SSE response, killing a
// healthy long-running agent mid-stream. Per-run bounds come from the request context instead.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg, http: &http.Client{}}
}

func (p *Provider) Name() string { return p.cfg.Name }

// List reports no discoverable sessions: AG-UI has no enumeration endpoint, and a thread id is
// meaningful only to the backend that minted it. Sessions here are always ones we created.
func (p *Provider) List(ctx context.Context) ([]protocol.Session, error) { return nil, nil }

// Create opens a session (an AG-UI thread) and starts its first run.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	s := &session{
		id:       p.cfg.Name + "_" + randID(),
		provider: p.cfg.Name,
		cwd:      cwd,
		prov:     p,
		events:   make(chan agent.Event, 64),
		done:     make(chan struct{}),
		tools:    map[string]*toolCall{},
	}
	if prompt != "" {
		go s.runTurn(prompt)
	}
	return s, nil
}

// toolCall accumulates one tool call across its START/ARGS/END/RESULT events.
//
// AG-UI streams a tool call as four event types with argument fragments arriving as raw JSON text;
// our protocol carries ONE session.tool frame per call, updated in place by id. So the fragments are
// folded here rather than forwarded, and the tool is re-emitted as it fills in.
type toolCall struct {
	name string
	args strings.Builder
}

type session struct {
	id       string
	provider string
	cwd      string
	prov     *Provider

	events chan agent.Event
	done   chan struct{}
	closed sync.Once
	// sendMu serialises sends against the close in Close.
	//
	// A bare `select { case events <- ev: case <-done: }` is NOT enough: select picks a ready case,
	// and a channel closed concurrently still looks ready to send, so the race is a panic rather than
	// a dropped event. Runs are goroutines that outlive a user closing the session, so this is
	// reachable in normal use, not just at shutdown.
	sendMu     sync.Mutex
	sendClosed bool

	mu sync.Mutex
	// threadID is the AG-UI thread this session maps onto — stable for the session's whole life, so
	// the backend can keep per-conversation state across runs.
	threadID string
	running  bool
	cancel   context.CancelFunc
	tools    map[string]*toolCall
	// pendingResume holds answers to interrupts raised by the last run. AG-UI resolves an interrupt
	// by ENDING the run and having the client start a new one carrying `resume`, so an approval
	// cannot be answered in place — it is banked here and replayed on the next run.
	pendingResume []resumeEntry
	// msg accumulates the current assistant message so a finalized session.message can be emitted at
	// TEXT_MESSAGE_END. Providers that never send END are covered by the run-end flush.
	msg strings.Builder
}

func (s *session) ID() string       { return s.id }
func (s *session) Provider() string { return s.provider }

func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) emit(ev agent.Event) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// Prompt starts a new run carrying the user's text.
func (s *session) Prompt(ctx context.Context, text string) error {
	s.mu.Lock()
	busy := s.running
	s.mu.Unlock()
	if busy {
		return fmt.Errorf("%s: a run is already in flight", s.provider)
	}
	go s.runTurn(text)
	return nil
}

// Respond banks an approval answer for replay on the next run.
//
// It does NOT resolve the approval against the backend directly, because AG-UI has no such call: an
// interrupt terminated the run that raised it, and the only way to answer is to start a new run with
// a matching `resume` entry. The follow-up run is kicked off here so the user experiences it as an
// in-place answer.
func (s *session) Respond(ctx context.Context, approvalID, decision string) error {
	status := "resolved"
	if decision != protocol.DecisionAllow {
		status = "cancelled"
	}
	s.mu.Lock()
	s.pendingResume = append(s.pendingResume, resumeEntry{
		InterruptID: approvalID,
		Status:      status,
		// The payload shape is the interrupt's own responseSchema, which for the confirmation case
		// AG-UI documents is {approved: bool}. Sending it for every reason is harmless — a backend
		// that declared a different schema reads the fields it asked for.
		Payload: map[string]any{"approved": decision == protocol.DecisionAllow},
	})
	busy := s.running
	s.mu.Unlock()
	if !busy {
		go s.runTurn("") // empty input: this run exists solely to deliver the resume
	}
	return nil
}

// Stop cancels the in-flight run's request, which ends its SSE stream.
func (s *session) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *session) Close() error {
	_ = s.Stop(context.Background())
	s.closed.Do(func() {
		// `done` first, UNLOCKED: it releases any emit currently blocked on a full channel, so taking
		// sendMu below cannot deadlock against a producer waiting for a reader that has gone away.
		close(s.done)
		s.sendMu.Lock()
		s.sendClosed = true
		close(s.events)
		s.sendMu.Unlock()
	})
	return nil
}

// Probe reports whether a run is genuinely in flight.
//
// This is what keeps an AG-UI session legible to the turn engine despite the protocol modelling no
// liveness of its own. `running` is set for exactly as long as the run's HTTP request is open, so it
// answers the reconciler's real question — "is this agent still working, or did we lose the
// completion event?" — from transport state rather than from stream inference.
func (s *session) Probe(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, nil
}

// runTurn performs one AG-UI run start to finish.
func (s *session) runTurn(input string) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		cancel()
		return
	}
	s.running, s.cancel = true, cancel
	if s.threadID == "" {
		s.threadID = s.id // the Iron Rain session id IS the thread id
	}
	req := runInput{
		ThreadID: s.threadID,
		RunID:    "run_" + randID(),
		Resume:   s.pendingResume,
	}
	s.pendingResume = nil
	if input != "" {
		req.Messages = []inputMessage{{Role: "user", Content: input}}
	}
	s.mu.Unlock()

	// Idempotent, so the explicit release below is safe and a panic still frees the session.
	release := func() {
		cancel()
		s.mu.Lock()
		s.running, s.cancel = false, nil
		s.mu.Unlock()
	}
	defer release()

	s.emit(agent.Event{Type: protocol.TypeSessionStatus,
		Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning}})

	status, err := s.stream(ctx, req)
	// Sampled BEFORE release, which cancels this very context: reading ctx.Err() afterwards always
	// reports Canceled, which would misclassify every genuine failure as a user-initiated Stop and
	// silently swallow it as a normal idle turn.
	stopped := ctx.Err() != nil

	// Release BEFORE announcing the turn ended. The hub reacts to a terminal status by sending the
	// next prompt, so emitting while `running` was still set produced a real race: a perfectly valid
	// follow-up was rejected with "a run is already in flight". The status is the signal that this
	// session is free, so it must not be published until it actually is.
	release()

	// An answer that arrived while this run was still finishing is banked but unsent — Respond saw
	// the session busy and declined to start a run. Delivering it here is what makes the handoff
	// race-free: whoever finishes last carries the resume, so an approval can never be stranded.
	s.mu.Lock()
	orphaned := len(s.pendingResume) > 0
	s.mu.Unlock()
	if orphaned && !stopped {
		go s.runTurn("")
		return
	}

	switch {
	case err != nil && !stopped:
		s.emit(agent.Event{Type: protocol.TypeSessionStatus,
			Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: err.Error()}})
	default:
		// A cancelled context is the user pressing Stop, not a failure — reporting it as an error
		// would page them about something they just did.
		if status == "" {
			status = protocol.StatusIdle
		}
		s.emit(agent.Event{Type: protocol.TypeSessionStatus,
			Payload: protocol.SessionStatus{SessionID: s.id, Status: status}})
	}
}

// stream POSTs the run and translates its SSE response until the run terminates.
func (s *session) stream(ctx context.Context, in runInput) (string, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.prov.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// JSON only. AG-UI's protobuf encoding exists but its schema trails the JSON one by seventeen
	// event types and its own docs recommend against it, so advertising it would invite a worse wire
	// format for no gain.
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range s.prov.cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := s.prov.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("%s returned HTTP %d: %s", s.prov.cfg.Name, resp.StatusCode, firstLine(string(snippet)))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue // comments, blank separators, and any `event:`/`id:` lines
		}
		done, status, err := s.handle(strings.TrimSpace(payload))
		if err != nil {
			return "", err
		}
		if done {
			return status, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	// The stream ended without a terminal event. Treat it as the run finishing rather than as an
	// error: a truncated stream with output already delivered is far more usefully shown as a
	// completed turn the turn engine can reconcile than as a failure.
	s.flushMessage("")
	return protocol.StatusIdle, nil
}

// handle translates one decoded AG-UI event. It reports whether the run has terminated.
func (s *session) handle(raw string) (done bool, terminal string, err error) {
	var ev aguiEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return false, "", nil // a malformed frame is skipped, never fatal to the run
	}
	switch ev.Type {
	case evRunStarted:
		return false, "", nil

	case evTextContent, evTextChunk:
		if ev.Delta == "" {
			return false, "", nil
		}
		s.mu.Lock()
		s.msg.WriteString(ev.Delta)
		s.mu.Unlock()
		s.emit(agent.Event{Type: protocol.TypeOutputDelta,
			Payload: protocol.OutputDelta{SessionID: s.id, Text: ev.Delta}})

	case evTextEnd:
		s.flushMessage(ev.MessageID)

	case evReasoningContent, evThinkingContent:
		if ev.Delta != "" {
			s.emit(agent.Event{Type: protocol.TypeThinking,
				Payload: protocol.Thinking{SessionID: s.id, Text: ev.Delta}})
		}

	case evToolStart:
		s.mu.Lock()
		s.tools[ev.ToolCallID] = &toolCall{name: ev.ToolCallName}
		s.mu.Unlock()
		s.emitTool(ev.ToolCallID, protocol.StatusRunning, "")

	case evToolArgs:
		s.mu.Lock()
		if tc := s.tools[ev.ToolCallID]; tc != nil {
			tc.args.WriteString(ev.Delta)
		}
		s.mu.Unlock()
		s.emitTool(ev.ToolCallID, protocol.StatusRunning, "")

	case evToolEnd:
		s.emitTool(ev.ToolCallID, protocol.StatusRunning, "")

	case evToolResult:
		s.emitTool(ev.ToolCallID, protocol.StatusDone, ev.Content)

	case evRunError:
		msg := ev.Message
		if msg == "" {
			msg = "the agent reported an error"
		}
		return true, "", fmt.Errorf("%s", msg)

	case evRunFinished:
		// An interrupt outcome is an approval request, not a completed turn: the run ends, and the
		// user's answer travels in the NEXT run's resume list (see Respond).
		if ev.Outcome != nil && ev.Outcome.Type == "interrupt" && len(ev.Outcome.Interrupts) > 0 {
			for _, it := range ev.Outcome.Interrupts {
				s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{
					ApprovalID: it.ID,
					SessionID:  s.id,
					Tool:       interruptTool(it),
					Detail:     it.Message,
				}})
			}
			return true, protocol.StatusAwaitingApproval, nil
		}
		s.flushMessage("")
		return true, protocol.StatusIdle, nil
	}
	return false, "", nil
}

// flushMessage emits the accumulated assistant text as a finalized message and resets the buffer.
func (s *session) flushMessage(msgID string) {
	s.mu.Lock()
	text := s.msg.String()
	s.msg.Reset()
	s.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return
	}
	s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
		SessionID: s.id, Role: "assistant", Text: text, MsgID: msgID,
	}})
}

// emitTool re-sends a tool card under its stable id. Our protocol updates one frame in place rather
// than streaming a start/args/result triple, so every fragment produces a fuller version of the same
// card instead of a new one.
func (s *session) emitTool(id, status, output string) {
	s.mu.Lock()
	tc := s.tools[id]
	var name, args string
	if tc != nil {
		name, args = tc.name, tc.args.String()
	}
	if status == protocol.StatusDone {
		delete(s.tools, id)
	}
	s.mu.Unlock()
	if name == "" {
		return // a result for a call we never saw start
	}
	s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
		SessionID: s.id, ID: id, Name: name, Title: toolTitle(name, args),
		Output: output, Status: status,
	}})
}

// toolTitle renders a one-line summary from the accumulated argument JSON.
//
// The arguments arrive as streamed fragments, so mid-call they are usually INVALID JSON — half an
// object. That is expected, not an error: until it parses, the tool name alone is the title.
func toolTitle(name, args string) string {
	if strings.TrimSpace(args) == "" {
		return name
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return name
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query"} {
		if v, ok := m[k].(string); ok && v != "" {
			return name + " · " + firstLine(v)
		}
	}
	return name
}

// interruptTool names the thing being approved. AG-UI carries the reason and an optional toolCallId
// but no tool NAME, so the reason is the honest fallback.
func interruptTool(it interrupt) string {
	if it.Reason != "" {
		return it.Reason
	}
	return "approval"
}

// randID mints a short random identifier, matching how the other adapters build session ids.
func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		// Back up to a rune boundary — a byte slice would split a multi-byte character and the
		// invalid UTF-8 becomes replacement characters when the event is encoded.
		n := 200
		for n > 0 && s[n]&0xC0 == 0x80 {
			n--
		}
		s = s[:n] + "…"
	}
	return s
}

// --- wire types -------------------------------------------------------------------------------

// runInput is AG-UI's RunAgentInput.
type runInput struct {
	ThreadID string         `json:"threadId"`
	RunID    string         `json:"runId"`
	Messages []inputMessage `json:"messages,omitempty"`
	Resume   []resumeEntry  `json:"resume,omitempty"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type resumeEntry struct {
	InterruptID string         `json:"interruptId"`
	Status      string         `json:"status"` // resolved | cancelled
	Payload     map[string]any `json:"payload,omitempty"`
}

// aguiEvent is the union of every field we read. AG-UI events are a flat, discriminated-by-`type`
// shape, so one permissive struct decodes all of them; fields absent for a given type stay zero.
type aguiEvent struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
	Message   string `json:"message"`

	ToolCallID   string `json:"toolCallId"`
	ToolCallName string `json:"toolCallName"`
	Content      string `json:"content"`

	Outcome *outcome `json:"outcome"`
}

type outcome struct {
	Type       string      `json:"type"` // success | interrupt
	Interrupts []interrupt `json:"interrupts"`
}

type interrupt struct {
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	ToolCallID string `json:"toolCallId"`
}
