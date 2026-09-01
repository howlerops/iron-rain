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

	// toolCompleted is the terminal status of a tool CARD, distinct from protocol.StatusDone which
	// belongs to a turn. The hub keys its outstanding-tool bookkeeping on this exact string.
	toolCompleted = "completed"

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
	// runGen identifies the run that currently owns `running`. A run's release must free ONLY its own
	// claim: the release is called explicitly before the terminal status is emitted (so the hub can
	// send the next prompt immediately) and again by defer, and a successor can have claimed the
	// session in between. Without an owner check that second release cleared the SUCCESSOR's claim,
	// letting a third run start concurrently with it.
	runGen uint64
	tools  map[string]*toolCall
	// pendingResume holds answers to interrupts raised by the last run. AG-UI resolves an interrupt
	// by ENDING the run and having the client start a new one carrying `resume`, so an approval
	// cannot be answered in place — it is banked here and replayed on the next run.
	pendingResume []resumeEntry
	// sawText / pendingBreak carry the MESSAGE BOUNDARY across a TEXT_MESSAGE_END. The accumulated
	// text buffer that used to live here is gone: it was written only from the delta handler and read
	// only to re-emit the same bytes as a finalized message, so it could never hold anything the
	// client had not already received.
	sawText      bool
	pendingBreak bool
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
//
// The claim is made HERE, synchronously, not inside the goroutine. Sampling `running`, dropping the
// lock and then letting runTurn re-check it meant a run that started in the gap made runTurn return
// silently — while Prompt had already reported success. The user's message was gone: accepted by the
// API, never sent to the backend, and nothing anywhere said so. Claiming before returning makes the
// answer honest either way.
func (s *session) Prompt(ctx context.Context, text string) error {
	req, rctx, release, ok := s.beginRun(text)
	if !ok {
		return fmt.Errorf("%s: a run is already in flight", s.provider)
	}
	go s.execRun(rctx, req, release)
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
	req, ctx, release, ok := s.beginRun(input)
	if !ok {
		return
	}
	s.execRun(ctx, req, release)
}

// beginRun claims the session for one run and builds that run's request, atomically. ok is false if a
// run is already in flight; on true the caller owns the returned release and must call it.
func (s *session) beginRun(input string) (runInput, context.Context, func(), bool) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		cancel()
		return runInput{}, nil, nil, false
	}
	s.running, s.cancel = true, cancel
	s.runGen++
	gen := s.runGen
	// Message-boundary state is per-TURN. A turn that ends on TEXT_MESSAGE_END leaves a break owed,
	// and the client renders each turn as its own row — so carrying it over would open the next reply
	// with a blank line. sawText likewise, or a turn whose first event is a stray END owes a break it
	// earned in the previous turn.
	//
	// A RESUME run is the exception: AG-UI answers an approval by ending the run and starting a new
	// one carrying `resume`, so the two runs are one logical turn and the client folds them into one
	// bubble. Clearing the debt there would fuse the text after the approval onto the sentence before
	// it ("...I'll need approval to run this.Done, it succeeded.").
	if s.pendingResume == nil {
		s.sawText, s.pendingBreak = false, false
	}
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

	// Safe to call more than once, and safe to call late: it frees the session only while THIS run
	// still owns the claim, so a release racing a successor cannot free the successor's.
	release := func() {
		cancel()
		s.mu.Lock()
		if s.runGen == gen {
			s.running, s.cancel = false, nil
		}
		s.mu.Unlock()
	}
	return req, ctx, release, true
}

// execRun streams one claimed run to completion and reports its terminal status.
func (s *session) execRun(ctx context.Context, req runInput, release func()) {
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
	s.endMessage()
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
		brk := s.pendingBreak
		s.pendingBreak = false
		s.sawText = true
		s.mu.Unlock()
		// Separate one assistant message from the next WITHIN a turn. The client folds deltas into a
		// single bubble until the turn ends, so without this the last word of one message fuses to the
		// first of the next — the same defect opencode, claude-code and pi each had to fix. A blank
		// line, because these are separate paragraphs and markdown folds a single newline back into one.
		if brk {
			s.emit(agent.Event{Type: protocol.TypeOutputDelta,
				Payload: protocol.OutputDelta{SessionID: s.id, Text: "\n\n"}})
		}
		s.emit(agent.Event{Type: protocol.TypeOutputDelta,
			Payload: protocol.OutputDelta{SessionID: s.id, Text: ev.Delta}})

	case evTextEnd:
		s.endMessage()

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
		// "completed", NOT protocol.StatusDone. StatusDone ("done") is a TURN status; a finished tool
		// card is "completed" everywhere else (pi, claude-code, opencode). The hub clears a card from
		// turnTools on `case "completed", "error"` only, so "done" left every AG-UI tool outstanding
		// for the whole turn — and turn close then SEALED it as Status "error" with the seal note in
		// place of its output. Every successful tool call ended up rendered, and persisted, as a
		// failure that had eaten its own result.
		s.emitTool(ev.ToolCallID, toolCompleted, ev.Content)

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
		s.endMessage()
		return true, protocol.StatusIdle, nil
	}
	return false, "", nil
}

// endMessage closes an assistant message without re-sending it.
//
// It used to emit the accumulated text as a finalized session.message — a verbatim restatement of the
// output.delta frames it had just sent, since the buffer had no other writer. That frame arrives after
// the client may already have sealed the streamed row, and the client only REPLACES a row that is
// still streaming, so it was appended instead: the same reply twice, adjacent and identical. Exactly
// the bug fixed in opencode's adapter, here with no guard at all.
//
// Durability is unaffected. The hub synthesizes and persists the turn's assistant text at idle for
// any provider that never finalizes one (hub.finalizeTurnTranscript), which is how claude-code, pi and
// the CLI family have always been stored — AG-UI simply joins them.
//
// What DOES have to survive is the boundary: TEXT_MESSAGE_END means "that message is complete", and
// the next one must not be glued to it.
func (s *session) endMessage() {
	s.mu.Lock()
	if s.sawText {
		s.pendingBreak = true
	}
	s.mu.Unlock()
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
	if status == toolCompleted {
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
