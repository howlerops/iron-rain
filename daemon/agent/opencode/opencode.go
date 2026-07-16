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

	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "opencode" }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) subscribe() error {
	ctx, cancel := context.WithCancel(context.Background())
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
	case "message.part.updated":
		var pr struct {
			Part struct {
				SessionID string `json:"sessionID"`
			} `json:"part"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal(e.Properties, &pr) != nil || pr.Part.SessionID != s.id || pr.Delta == "" {
			return
		}
		s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: pr.Delta}})

	case "permission.updated":
		var perm struct {
			ID        string          `json:"id"`
			Type      string          `json:"type"`
			SessionID string          `json:"sessionID"`
			Title     string          `json:"title"`
			Metadata  json.RawMessage `json:"metadata"`
		}
		if json.Unmarshal(e.Properties, &perm) != nil || perm.SessionID != s.id {
			return
		}
		tool := perm.Type
		if tool == "" {
			tool = perm.Title
		}
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
		s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: perm.ID, SessionID: s.id, Tool: tool, Input: perm.Metadata}})

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

// Prompt sends a follow-up. TODO: match opencode's full message body (model/agent);
// v0 sends a single text part, which is sufficient for a default-configured server.
func (s *session) Prompt(ctx context.Context, text string) error {
	body := map[string]any{"parts": []map[string]any{{"type": "text", "text": text}}}
	return s.p.postJSON(ctx, "/session/"+s.id+"/message", body, nil)
}

// Respond maps allow->"once", deny->"reject".
func (s *session) Respond(ctx context.Context, approvalID, decision string) error {
	resp := "reject"
	if decision == protocol.DecisionAllow {
		resp = "once"
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
