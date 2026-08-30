package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/howlerops/oculus/daemon/genui"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/textutil"
)

// Moving an opencode conversation backwards.
//
// This adapter was declared with NO thread operations, on the assumption that opencode's server had
// no branch or rewind endpoint. That assumption was never checked, and it was wrong: opencode has
// the richest thread API of any provider here —
//
//	POST /session/{id}/fork      {messageID}   branch into a NEW session at a message
//	POST /session/{id}/revert    {messageID}   move THIS session back to a message
//	POST /session/{id}/unrevert                undo the revert
//	GET  /session/{id}/children                sessions forked from this one
//
// revert is a real rewind, and unrevert is something no other provider offers: the rewind itself is
// undoable. Every call is scoped with withDir — opencode partitions all HTTP by ?directory=, and an
// unscoped call lands on the wrong session's history or none at all.

// Capabilities is overridden here rather than in opencode.go so the thread declaration sits next to
// the code that has to honour it.
func (s *session) threadCaps() protocol.ThreadCaps {
	return protocol.ThreadCaps{
		Tree:     true,
		Fork:     true,
		Rewind:   true,
		Compact:  true,
		Unrevert: true,
		// Summarize is false: opencode reverts without producing a summary of what it left behind.
		// pi's branch summary has no equivalent here, and claiming one would promise a carry-forward
		// that never happens.
	}
}

// ocMessage is the subset of opencode's message shape a fork/revert picker needs.
type ocMessage struct {
	Info struct {
		ID   string `json:"id"`
		Role string `json:"role"`
		Time struct {
			Created int64 `json:"created"`
		} `json:"time"`
	} `json:"info"`
	Parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"parts"`
}

// preview is the first line of what the USER actually wrote.
//
// The generative-UI guide is folded into a session's first user turn, so the raw text of that turn
// begins with the ⟦iron:ui-guide⟧ block. Previewing it unstripped made the first — and often only —
// row of the branch picker read "⟦iron:ui-guide⟧" instead of the prompt, which is both useless and
// alarming: it looks like the conversation started with something you did not send. Found by opening
// this picker on a real session rather than by reading the code.
func (m ocMessage) preview() string {
	for _, p := range m.Parts {
		if p.Type != "text" {
			continue
		}
		t := strings.TrimSpace(genui.StripGuide(p.Text))
		if t != "" {
			return textutil.FirstLine(t, 120)
		}
	}
	return ""
}

// ThreadTree lists the points this conversation can be forked or reverted to (agent.ThreadOps).
//
// USER messages only. opencode's message list includes every assistant turn and tool part, but both
// endpoints take a messageID that means "the turn starting here" — offering assistant messages would
// present destinations whose behaviour is not the one the label implies.
func (s *session) ThreadTree(ctx context.Context) ([]protocol.ThreadNode, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.p.baseURL+withDir("/session/"+s.id+"/message", s.dir), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.p.unary.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("opencode returned %s listing this session's messages", resp.Status)
	}
	var msgs []ocMessage
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("opencode returned an unreadable message list: %w", err)
	}

	nodes := make([]protocol.ThreadNode, 0, len(msgs))
	for _, m := range msgs {
		if m.Info.Role != "user" || m.Info.ID == "" {
			continue
		}
		preview := m.preview()
		if preview == "" {
			preview = "(no text)"
		}
		nodes = append(nodes, protocol.ThreadNode{
			ID:      m.Info.ID,
			Kind:    "user",
			Preview: preview,
			At:      m.Info.Time.Created / 1000, // opencode reports milliseconds
			OnPath:  true,                       // this session's own past; siblings live in /children
		})
	}
	if len(nodes) > 0 {
		nodes[len(nodes)-1].Current = true
	}
	return nodes, nil
}

// ThreadFork branches into a NEW session at nodeID (agent.ThreadOps).
//
// Unlike pi's fork, which rebinds the same session, opencode's returns a DIFFERENT session id. That
// id is what the caller must attach to — returning this session's would silently point the user at
// the unforked original.
func (s *session) ThreadFork(ctx context.Context, nodeID string) (string, error) {
	if strings.TrimSpace(nodeID) == "" {
		return "", fmt.Errorf("forking needs the message to branch from")
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := s.p.postJSON(ctx, withDir("/session/"+s.id+"/fork", s.dir),
		map[string]any{"messageID": nodeID}, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("opencode forked the session but reported no new session id")
	}
	return created.ID, nil
}

// ThreadRewind moves THIS session back to nodeID (agent.ThreadOps).
//
// opencode calls it revert, and it is undoable via unrevert — see Unrevert.
func (s *session) ThreadRewind(ctx context.Context, nodeID string) error {
	if strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("rewinding needs the message to go back to")
	}
	return s.p.postJSON(ctx, withDir("/session/"+s.id+"/revert", s.dir),
		map[string]any{"messageID": nodeID}, nil)
}

// Unrevert undoes the last rewind.
//
// Not part of agent.ThreadOps because no other provider can do it, and widening the shared interface
// for a single implementation would force three "not supported" stubs. Reached through a type
// assertion by the one caller that offers the control.
func (s *session) Unrevert(ctx context.Context) error {
	return s.p.postJSON(ctx, withDir("/session/"+s.id+"/unrevert", s.dir), map[string]any{}, nil)
}

// PreviewForTest exposes the preview derivation for tests. The stripping it performs is the
// difference between a picker showing your prompt and one showing an injected preamble, which is
// worth a test that does not need a live server.
func PreviewForTest(text string) string {
	return ocMessage{Parts: []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: text}}}.preview()
}
