package hub

import (
	"context"
	"log"
	"strings"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Session modes are enforced HERE, at the approval layer, rather than being delegated to each
// harness. Every tool call already transits the hub, so one implementation covers opencode,
// claude-code, pi and any BYO CLI identically — including harnesses with no native permission mode
// at all. Where a harness does have one, the daemon additionally forwards it as a hint
// (agent.ModeSetter) so the model itself knows it should be planning rather than editing.
//
// This is deliberately the inverse of how editors usually do it: enforcing in the client means every
// new harness needs new enforcement code and any harness the editor doesn't control is unprotected.

// mutatingTools are tool names that can change the user's machine. Names are compared
// case-insensitively because harnesses disagree on casing (opencode "edit", claude-code "Edit").
var mutatingTools = map[string]bool{
	"edit": true, "write": true, "patch": true, "apply_patch": true, "applypatch": true,
	"bash": true, "shell": true, "run": true, "execute": true, "terminal": true,
	"notebookedit": true, "multiedit": true, "create": true, "delete": true, "move": true,
	"webfetch": false, // reading the network is not mutating the machine
	// The preview tools. Clicking and typing drive the user's running app, which can submit a form
	// or fire a request, so they are mutating; looking at the rendered page is not.
	"preview_click": true, "preview_fill": true, "preview_snapshot": false,
}

// isMutatingTool reports whether a tool can modify the machine. Unknown tools are treated as
// mutating in read-only modes: failing closed is the only safe default when a harness invents a tool
// name we've never seen.
func isMutatingTool(tool string) bool {
	t := strings.ToLower(strings.TrimSpace(tool))
	t = strings.TrimPrefix(t, "[sub-agent] ")
	if v, ok := mutatingTools[t]; ok {
		return v
	}
	// Read-ish tools are a small, stable, well-known set across every harness.
	switch t {
	case "read", "view", "list", "ls", "glob", "grep", "search", "find", "todowrite", "todoread",
		"websearch", "fetch", "lsp", "diagnostics", "think", "exitplanmode", "task", "question":
		return false
	}
	return true
}

// modeDeniesTool reports whether the session's mode forbids a tool outright.
func modeDeniesTool(mode, tool string) bool {
	switch mode {
	case protocol.ModeAsk, protocol.ModeArchitect:
		return isMutatingTool(tool)
	default:
		return false
	}
}

// normalizeMode maps input (including the legacy Plan bool) onto a known mode.
func normalizeMode(mode string, plan bool) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case protocol.ModeAsk:
		return protocol.ModeAsk
	case protocol.ModeArchitect, "plan":
		return protocol.ModeArchitect
	case protocol.ModeCode, "build", "":
		if mode == "" && plan {
			return protocol.ModeArchitect // the pre-mode client's "plan mode" checkbox
		}
		return protocol.ModeCode
	default:
		return protocol.ModeCode
	}
}

// setSessionMode switches a live session's mode and, when the harness supports it, tells the harness
// too. Enforcement does not depend on the harness accepting the hint.
func (h *Hub) setSessionMode(ctx context.Context, m *managedSession, mode string) {
	mode = normalizeMode(mode, false)
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
	if ms, ok := m.sess.(agent.ModeSetter); ok {
		if err := ms.SetMode(ctx, mode); err != nil {
			log.Printf("session %s: harness rejected mode %q (%v) — daemon-side enforcement still applies", m.sess.ID(), mode, err)
		}
	}
	h.persistSession(m)
}

// sessionMode reads a session's current mode, defaulting to code.
func (m *managedSession) sessionMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "" {
		return protocol.ModeCode
	}
	return m.mode
}

// emitTool surfaces a synthetic tool note in the transcript — used to make a mode-blocked tool call
// VISIBLE. Silently denying would leave the user watching an agent that mysteriously refuses to act.
func (m *managedSession) emitTool(text string) {
	ev := agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
		SessionID: m.sess.ID(),
		ID:        "mode-block:" + randToken(),
		Name:      text,
		Status:    "completed",
	}}
	if raw, err := ev.Encode(); err == nil {
		m.broadcast(raw)
	}
}
