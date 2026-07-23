// Package opencode implements agent.Provider against a running `opencode serve`
// HTTP/SSE API (endpoints POST /session, POST /session/{id}/message, GET /event,
// POST /session/{id}/permissions/{permissionID}, POST /session/{id}/abort).
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// unaryTimeout bounds the request/response (non-SSE) calls so a hung opencode server
// can't block a goroutine (e.g. a POST /message fired with the long-lived subscribe
// ctx) indefinitely.
const unaryTimeout = 30 * time.Second

// Provider talks to one opencode server.
type Provider struct {
	baseURL string
	http    *http.Client // no Timeout: for the long-lived SSE /event stream only
	unary   *http.Client // bounded Timeout: for request/response List/postJSON/replayHistory
}

// New returns a Provider for the given opencode base URL (e.g. http://127.0.0.1:4096).
func New(baseURL string) *Provider {
	return &Provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{},
		unary:   &http.Client{Timeout: unaryTimeout},
	}
}

func (p *Provider) Name() string { return "opencode" }

// List returns current sessions.
func (p *Provider) List(ctx context.Context) ([]protocol.Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/session", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.unary.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Time  struct {
			Updated int64 `json:"updated"` // opencode reports millis
		} `json:"time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]protocol.Session, 0, len(raw))
	for _, s := range raw {
		out = append(out, protocol.Session{
			ID: s.ID, Provider: "opencode", Status: protocol.StatusIdle, Title: s.Title,
			UpdatedAt: s.Time.Updated / 1000, // millis -> seconds
		})
	}
	return out, nil
}

// Models lists every model opencode has configured, across its providers (GET /config/providers),
// so the app can offer a picker. The model id must be paired with its providerID when sent.
func (p *Provider) Models(ctx context.Context) ([]protocol.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/config/providers", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.unary.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Providers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models map[string]struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []protocol.ModelInfo
	for _, pr := range raw.Providers {
		for _, m := range pr.Models {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			out = append(out, protocol.ModelInfo{ID: m.ID, Name: pr.Name + " · " + name, Provider: pr.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Create starts a session and (if prompt != "") kicks it off.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.create(ctx, cwd, prompt, "")
}

// CreatePlan starts a session that runs turns as opencode's "plan" agent — edits and bash are
// gated on approval, so the agent proposes/plans and nothing changes until you allow it.
func (p *Provider) CreatePlan(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.create(ctx, cwd, prompt, "plan")
}

func (p *Provider) create(ctx context.Context, cwd, prompt, agentName string) (agent.Session, error) {
	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := p.postJSON(ctx, withDir("/session", cwd), map[string]any{}, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, fmt.Errorf("opencode: create returned empty session id")
	}

	s := &session{p: p, id: created.ID, dir: cwd, agent: agentName, events: make(chan agent.Event, 32), done: make(chan struct{})}
	if err := s.subscribe(); err != nil {
		return nil, err
	}
	if prompt != "" {
		if err := s.Prompt(ctx, prompt); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Attach connects to an existing session (discovered on the host): it subscribes to
// live events and replays the session's message history so the app shows the
// conversation and can continue it.
func (p *Provider) Attach(ctx context.Context, sessionID, cwd string) (agent.Session, error) {
	// cwd scopes the /event subscription + message writes to the session's directory
	// (opencode partitions both by ?directory=). Empty cwd falls back to the server's
	// default directory — correct only for sessions that live there.
	s := &session{p: p, id: sessionID, dir: cwd, events: make(chan agent.Event, 64), done: make(chan struct{})}
	if err := s.subscribe(); err != nil {
		return nil, err
	}
	s.replayHistory(ctx)
	return s, nil
}

// replayHistory fetches the session's messages and emits them as SessionMessage
// events (oldest first) so the client can render the existing conversation.
func (s *session) replayHistory(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.p.baseURL+withDir("/session/"+s.id+"/message", s.dir), nil)
	if err != nil {
		return
	}
	resp, err := s.p.unary.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var msgs []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Tool string `json:"tool"`
		} `json:"parts"`
	}
	if json.NewDecoder(resp.Body).Decode(&msgs) != nil {
		return
	}
	for _, m := range msgs {
		var text string
		var tool string
		for _, part := range m.Parts {
			switch part.Type {
			case "text":
				text += part.Text
			case "tool":
				if tool == "" {
					tool = part.Tool
				}
			}
		}
		if text != "" {
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: m.Info.Role, Text: text}})
		} else if tool != "" {
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: "tool", Text: tool}})
		}
	}
}

func (p *Provider) postJSON(ctx context.Context, path string, body, out any) error {
	return p.doPost(ctx, path, body, out, p.unary)
}

// postJSONLong is postJSON on the UN-TIMED client, for requests opencode intentionally blocks on for
// the entire agent turn (the message POST returns only when the turn yields — minutes for a big
// plan/multi-agent run). The 30s unary bound would spuriously fail those with "context deadline
// exceeded"; the session's own ctx still cancels this when the session closes.
func (p *Provider) postJSONLong(ctx context.Context, path string, body, out any) error {
	return p.doPost(ctx, path, body, out, p.http)
}

func (p *Provider) doPost(ctx context.Context, path string, body, out any, client *http.Client) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("opencode POST %s: %s", path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type session struct {
	p      *Provider
	id     string
	dir    string // working directory; forwarded to opencode as ?directory= (scopes the session)
	agent  string // opencode agent to run turns as ("plan" = gate edits/bash on approval); "" = default
	events chan agent.Event

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}

	modelMu       sync.Mutex // guards the selected model (set from the hub, read in sendParts)
	modelID       string
	modelProvider string

	// populated in the (single) readEvents goroutine — no mutex needed.
	msgRoles    map[string]string // messageID -> role (from message.updated)
	emittedUser map[string]bool   // messageIDs already forwarded as a user turn
	usageDone   map[string]bool   // messageIDs whose usage was already emitted (once per turn)
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "opencode" }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) subscribe() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	body, err := s.openEvents(ctx)
	if err != nil {
		cancel()
		return err
	}
	go s.readLoop(body)
	return nil
}

// openEvents opens the SSE /event stream, scoped to this session's directory. opencode
// partitions /event by ?directory= (exactly like POST /session and POST /message): a
// session in a project folder or worktree emits its events ONLY on /event?directory=<dir>,
// so a bare /event (the server's default directory) would silently miss them.
func (s *session) openEvents(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.p.baseURL+withDir("/event", s.dir), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.p.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("opencode /event: %s", resp.Status)
	}
	return resp.Body, nil
}

// readLoop drains the event stream, and — because an opencode session lives on the server
// independent of any one SSE connection — transparently reconnects if the stream drops
// (opencode restart, network blip, idle timeout) instead of ending the session. It only
// stops (and closes s.events, ending the session) when the session is Closed/Stopped.
func (s *session) readLoop(body io.ReadCloser) {
	defer close(s.events) // single sender; closing ends the session in the hub's run()
	for {
		s.scanEvents(body)
		body.Close()
		if s.ctx.Err() != nil {
			return // Close()/Stop() was called — the session is really done
		}
		// The stream dropped but the session still lives server-side: reconnect with a
		// capped backoff, then resume. (Any turn in flight keeps running server-side.)
		nb, ok := s.reconnectEvents()
		if !ok {
			return
		}
		log.Printf("opencode: /event reconnected sid=%s", s.id)
		body = nb
	}
}

// reconnectEvents retries openEvents with exponential backoff until it succeeds or the
// session is closed. Returns (stream, true) on success, (nil, false) if the session ended.
func (s *session) reconnectEvents() (io.ReadCloser, bool) {
	backoff := 500 * time.Millisecond
	start := time.Now()
	warned := false
	for {
		if s.ctx.Err() != nil {
			return nil, false
		}
		if body, err := s.openEvents(s.ctx); err == nil {
			if warned { // recovered — clear the error state back to running
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
					SessionID: s.id, Status: protocol.StatusRunning, Detail: "opencode reconnected"}})
			}
			return body, true
		}
		// Don't retry forever in silence: if the backend stays unreachable, surface the session as
		// errored once (the app/session.list then shows "error" instead of a phantom "running"),
		// while still retrying so it self-heals if opencode comes back.
		if !warned && time.Since(start) > 20*time.Second {
			warned = true
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
				SessionID: s.id, Status: protocol.StatusError, Detail: "opencode backend unreachable"}})
		}
		select {
		case <-s.ctx.Done():
			return nil, false
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (s *session) scanEvents(body io.ReadCloser) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	dataPrefix := []byte("data:")
	for sc.Scan() {
		// Work in bytes to avoid a string alloc + a []byte copy per streamed event.
		// sc.Bytes() is only valid until the next Scan(), which is fine because handle()
		// consumes it synchronously (json.Unmarshal does not retain the slice).
		line := sc.Bytes()
		if !bytes.HasPrefix(line, dataPrefix) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, dataPrefix))
		if len(payload) != 0 {
			s.handle(payload)
		}
	}
}

// handle translates one opencode SSE event into protocol events for this session.
func (s *session) handle(raw []byte) {
	var e struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return
	}
	switch e.Type {
	case "message.updated":
		// Record messageID -> role so we can tell user turns from assistant turns, and — for a
		// completed assistant message — emit token/cost usage (opencode carries it on info).
		var mu struct {
			Info struct {
				ID     string  `json:"id"`
				Role   string  `json:"role"`
				Cost   float64 `json:"cost"`
				Tokens struct {
					Input  int `json:"input"`
					Output int `json:"output"`
					Cache  struct {
						Read  int `json:"read"`
						Write int `json:"write"`
					} `json:"cache"`
				} `json:"tokens"`
				Time struct {
					Completed int64 `json:"completed"`
				} `json:"time"`
			} `json:"info"`
		}
		if json.Unmarshal(e.Properties, &mu) == nil && mu.Info.ID != "" {
			if s.msgRoles == nil {
				s.msgRoles = map[string]string{}
			}
			s.msgRoles[mu.Info.ID] = mu.Info.Role
			// One clean usage number per assistant turn, once the turn has completed. Guard
			// against re-emitting for the same message id (message.updated fires repeatedly).
			if mu.Info.Role == "assistant" && mu.Info.Time.Completed != 0 &&
				(mu.Info.Cost > 0 || mu.Info.Tokens.Input > 0 || mu.Info.Tokens.Output > 0) {
				if s.usageDone == nil {
					s.usageDone = map[string]bool{}
				}
				if !s.usageDone[mu.Info.ID] {
					s.usageDone[mu.Info.ID] = true
					in := mu.Info.Tokens.Input + mu.Info.Tokens.Cache.Read + mu.Info.Tokens.Cache.Write
					s.emit(agent.Event{Type: protocol.TypeSessionUsage, Payload: protocol.SessionUsage{
						SessionID: s.id, InputTokens: in, OutputTokens: mu.Info.Tokens.Output, CostUSD: mu.Info.Cost}})
				}
			}
		}

	case "message.part.updated":
		var pu struct {
			Part struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				Tool      string `json:"tool"`
				MessageID string `json:"messageID"`
				SessionID string `json:"sessionID"`
				State     struct {
					Status string `json:"status"`
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal(e.Properties, &pu) != nil || pu.Part.SessionID != s.id {
			return
		}
		switch pu.Part.Type {
		case "tool":
			// A tool that's running/completed → the agent is doing work; surface it as
			// activity ("running bash") and mark the session running, which clears any
			// pending approval on EVERY attached client once it's resolved.
			if pu.Part.State.Status == "running" || pu.Part.State.Status == "completed" {
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
					SessionID: s.id, Status: protocol.StatusRunning, Detail: "running " + pu.Part.Tool,
				}})
			}
		case "text":
			// Forward USER turns (so every attached client shows a prompt from any client;
			// assistant text streams via message.part.delta). Once per message.
			if pu.Part.Text == "" || s.msgRoles[pu.Part.MessageID] != "user" {
				return
			}
			if s.emittedUser == nil {
				s.emittedUser = map[string]bool{}
			}
			if s.emittedUser[pu.Part.MessageID] {
				return
			}
			s.emittedUser[pu.Part.MessageID] = true
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: "user", Text: pu.Part.Text}})
		}

	case "message.part.delta":
		// opencode streams assistant output as {sessionID, field, delta}. field "text"
		// is the answer; field "reasoning" is the thinking ("it's working").
		var pr struct {
			SessionID string `json:"sessionID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if json.Unmarshal(e.Properties, &pr) != nil || pr.SessionID != s.id || pr.Delta == "" {
			return
		}
		switch pr.Field {
		case "text":
			s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: pr.Delta}})
		case "reasoning":
			s.emit(agent.Event{Type: protocol.TypeThinking, Payload: protocol.Thinking{SessionID: s.id, Text: pr.Delta}})
		}

	case "todo.updated":
		// opencode's todowrite tool publishes a dedicated todo.updated bus event with the
		// full list — map it to the normalized session.todos.
		var tu struct {
			SessionID string `json:"sessionID"`
			Todos     []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		if json.Unmarshal(e.Properties, &tu) != nil || tu.SessionID != s.id {
			return
		}
		todos := make([]protocol.Todo, len(tu.Todos))
		for i, td := range tu.Todos {
			todos[i] = protocol.Todo{Content: td.Content, Status: td.Status}
		}
		s.emit(agent.Event{Type: protocol.TypeSessionTodos, Payload: protocol.SessionTodos{SessionID: s.id, Todos: todos}})

	case "permission.asked", "permission.updated":
		// opencode 1.17.x emits `permission.asked` with properties.permission (the tool);
		// older builds used `permission.updated` with properties.type/title. Handle both.
		// The reply endpoint (POST /session/{id}/permissions/{permID} once|reject) is
		// unchanged — verified live vs 1.17.19.
		var perm struct {
			ID         string          `json:"id"`
			SessionID  string          `json:"sessionID"`
			Permission string          `json:"permission"`
			Type       string          `json:"type"`
			Title      string          `json:"title"`
			Patterns   []string        `json:"patterns"`
			Metadata   json.RawMessage `json:"metadata"`
		}
		if json.Unmarshal(e.Properties, &perm) != nil || perm.SessionID != s.id {
			return
		}
		tool := perm.Permission
		if tool == "" {
			tool = perm.Type
		}
		if tool == "" {
			tool = perm.Title
		}
		// todowrite/todoread are bookkeeping, not code changes — don't surface them as
		// approvals (they'd pop a spurious card); the list arrives via todo.updated.
		if tool == "todowrite" || tool == "todoread" {
			if perm.ID != "" {
				go func() { _ = s.Respond(context.Background(), perm.ID, protocol.DecisionAllow) }()
			}
			return
		}
		// Detail: the concrete command/args to show inline (e.g. the bash command).
		detail := ""
		if len(perm.Patterns) > 0 {
			detail = perm.Patterns[0]
		}
		if detail == "" {
			var md struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(perm.Metadata, &md) == nil {
				detail = md.Command
			}
		}
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
		s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: perm.ID, SessionID: s.id, Tool: tool, Detail: detail, Input: perm.Metadata}})

	case "session.idle":
		var pr struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(e.Properties, &pr) != nil || pr.SessionID != s.id {
			return
		}
		// The turn is done: no message is streaming, so the per-message role/dedup
		// bookkeeping is no longer needed. Dropping it here bounds these maps to at most
		// one turn's worth of message IDs instead of growing for the session's lifetime.
		s.msgRoles = nil
		s.emittedUser = nil
		s.usageDone = nil
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
	}
}

func (s *session) emit(ev agent.Event) {
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// Prompt sends a message. opencode's POST /message blocks server-side until the
// turn yields (e.g. it parks on a tool-permission ask), so we fire it async and let
// progress arrive over SSE — otherwise the caller would deadlock, unable to answer
// the very approval the turn is waiting on. Errors surface as an error status event.
// v0 sends a single text part, sufficient for a default-configured server.
func (s *session) Prompt(_ context.Context, text string) error {
	return s.sendParts([]map[string]any{{"type": "text", "text": text}})
}

// PromptImages sends a multimodal turn: a text part + opencode "file" parts carrying each
// image as a base64 data URL (opencode decodes data: URLs directly).
func (s *session) PromptImages(_ context.Context, text string, images []protocol.ImageAttachment) error {
	parts := []map[string]any{}
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for i, im := range images {
		parts = append(parts, map[string]any{
			"type":     "file",
			"mime":     im.Mime,
			"filename": fmt.Sprintf("image-%d%s", i+1, extForMime(im.Mime)),
			"url":      "data:" + im.Mime + ";base64," + im.Data,
		})
	}
	return s.sendParts(parts)
}

// sendParts fires a message with the given parts asynchronously (opencode's POST blocks
// until the turn yields, so we drive progress from SSE — see the note above).
// SetModel selects the model for subsequent turns (opencode takes it per message).
func (s *session) SetModel(provider, model string) error {
	s.modelMu.Lock()
	s.modelProvider, s.modelID = provider, model
	s.modelMu.Unlock()
	return nil
}

func (s *session) sendParts(parts []map[string]any) error {
	body := map[string]any{"parts": parts}
	if s.agent != "" {
		body["agent"] = s.agent // e.g. "plan" — gate edits/bash on approval
	}
	s.modelMu.Lock()
	if s.modelID != "" {
		m := map[string]any{"modelID": s.modelID}
		if s.modelProvider != "" {
			m["providerID"] = s.modelProvider
		}
		body["model"] = m
	}
	s.modelMu.Unlock()
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		// The message POST blocks for the WHOLE turn, so a tight bound would spuriously fail long
		// plan/multi-agent turns (that was the old 30s bug). But leaving it UNBOUNDED means a genuinely
		// hung opencode turn sits silently forever — no log, no error, the app stuck "thinking". So:
		// bound it generously (30 min — longer than any real single turn) and LOG start + outcome, so
		// a hang both surfaces as an error the app can clear AND is visible in the log.
		pctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		start := time.Now()
		log.Printf("opencode: POST message sid=%s (turn start)", s.id)
		err := s.p.doPost(pctx, withDir("/session/"+s.id+"/message", s.dir), body, nil, s.p.http) // pctx bounds it
		if err != nil && ctx.Err() == nil {
			detail := err.Error()
			if pctx.Err() == context.DeadlineExceeded {
				detail = "the turn ran past 30 min with no result — it may be stuck; try again or interrupt"
			}
			log.Printf("opencode: POST message sid=%s FAILED after %s: %v", s.id, time.Since(start).Round(time.Second), err)
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: "opencode: " + detail}})
		} else if err == nil {
			log.Printf("opencode: POST message sid=%s turn returned after %s", s.id, time.Since(start).Round(time.Second))
		}
	}()
	return nil
}

func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

// Respond maps allow->"once", always->"always", deny->"reject".
func (s *session) Respond(ctx context.Context, approvalID, decision string) error {
	resp := "reject"
	switch decision {
	case protocol.DecisionAllow:
		resp = "once"
	case protocol.DecisionAlways:
		resp = "always"
	}
	return s.p.postJSON(ctx, withDir(fmt.Sprintf("/session/%s/permissions/%s", s.id, approvalID), s.dir), map[string]string{"response": resp}, nil)
}

func (s *session) Stop(ctx context.Context) error {
	return s.p.postJSON(ctx, withDir("/session/"+s.id+"/abort", s.dir), map[string]any{}, nil)
}

// withDir appends opencode's ?directory= query param (which scopes a call to a project
// folder / worktree) when dir is non-empty; empty dir → the server's default directory.
func withDir(path, dir string) string {
	if dir == "" {
		return path
	}
	return path + "?directory=" + url.QueryEscape(dir)
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
