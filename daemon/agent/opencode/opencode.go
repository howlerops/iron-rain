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
	"net/http"
	"strings"
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Provider talks to one opencode server.
type Provider struct {
	baseURL string
	http    *http.Client
}

// New returns a Provider for the given opencode base URL (e.g. http://127.0.0.1:4096).
func New(baseURL string) *Provider {
	return &Provider{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{}}
}

func (p *Provider) Name() string { return "opencode" }

// List returns current sessions.
func (p *Provider) List(ctx context.Context) ([]protocol.Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/session", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]protocol.Session, 0, len(raw))
	for _, s := range raw {
		out = append(out, protocol.Session{ID: s.ID, Provider: "opencode", Status: protocol.StatusIdle, Title: s.Title})
	}
	return out, nil
}

// Create starts a session and (if prompt != "") kicks it off.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := p.postJSON(ctx, "/session", map[string]any{}, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, fmt.Errorf("opencode: create returned empty session id")
	}

	s := &session{p: p, id: created.ID, events: make(chan agent.Event, 32), done: make(chan struct{})}
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

func (p *Provider) postJSON(ctx context.Context, path string, body, out any) error {
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
	resp, err := p.http.Do(req)
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
	events chan agent.Event

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "opencode" }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) subscribe() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.p.baseURL+"/event", nil)
	if err != nil {
		cancel()
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.p.http.Do(req)
	if err != nil {
		cancel()
		return err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return fmt.Errorf("opencode /event: %s", resp.Status)
	}
	go s.readEvents(resp.Body)
	return nil
}

func (s *session) readEvents(body io.ReadCloser) {
	defer close(s.events) // single sender; safe to close on exit
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload != "" {
			s.handle([]byte(payload))
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
	case "message.part.delta":
		// Real opencode streams assistant tokens as message.part.delta events with a
		// top-level {sessionID, field, delta}. (message.part.updated carries the full
		// accumulated part — incl. the echoed user prompt — so we stream deltas only,
		// avoiding duplication. Verified vs opencode 1.17.19.)
		var pr struct {
			SessionID string `json:"sessionID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if json.Unmarshal(e.Properties, &pr) != nil || pr.SessionID != s.id || pr.Field != "text" || pr.Delta == "" {
			return
		}
		s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: pr.Delta}})

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
	body := map[string]any{"parts": []map[string]any{{"type": "text", "text": text}}}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		if err := s.p.postJSON(ctx, "/session/"+s.id+"/message", body, nil); err != nil && ctx.Err() == nil {
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError}})
		}
	}()
	return nil
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
	return s.p.postJSON(ctx, fmt.Sprintf("/session/%s/permissions/%s", s.id, approvalID), map[string]string{"response": resp}, nil)
}

func (s *session) Stop(ctx context.Context) error {
	return s.p.postJSON(ctx, "/session/"+s.id+"/abort", map[string]any{}, nil)
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
