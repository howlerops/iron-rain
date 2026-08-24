package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Bringing MCP tools under the SAME approval rules and modes as native tools.
//
// The gateway sees an HTTP request, not a session — so on its own it could only allow or refuse MCP
// calls wholesale. That is a real hole: an agent in read-only mode could still reach out through an
// MCP server and write, and a user's carefully scoped "always allow" rules simply didn't apply.
//
// The fix is identity. Each session is injected with its OWN gateway token, so a call arriving with
// that token is attributable to that session — and from there the existing machinery works unchanged:
// the mode gate first, then the rule engine, then a real approval the user answers.

// mcpApprovalTimeout bounds how long an MCP tool call waits for a human. Long enough to walk to your
// phone, short enough that a forgotten prompt doesn't pin an agent turn forever.
const mcpApprovalTimeout = 10 * time.Minute

// argSummaryMax bounds the rendered arguments on an approval card. A card is a one-line decision
// surface and an argument object can carry a whole file's contents.
const argSummaryMax = 160

// argSummary renders a tool's arguments for the approval card, so a person can see WHAT is being
// asked and not merely which tool is asking. "write" is not a decision anyone can make; "write
// file_path=/repo/.git/hooks/pre-commit" is.
//
// Only what a HUMAN reads is truncated here — the full arguments still reach the guard and the rule
// engine through ApprovalRequest.Input, so a path hidden past the cutoff is still inspected.
func argSummary(args json.RawMessage) string {
	var obj map[string]any
	if len(args) == 0 || json.Unmarshal(args, &obj) != nil || len(obj) == 0 {
		return ""
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the same call must render the same card text every time
	var b strings.Builder
	for _, k := range keys {
		if b.Len() >= argSummaryMax {
			b.WriteString(", …")
			break
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(oneLine(fmt.Sprint(obj[k])))
	}
	return " — " + b.String()
}

// oneLine flattens a value so a multi-line argument can't turn one card into a wall of text, and so
// an argument containing newlines cannot visually forge additional card fields.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}

// mcpSessionTokens maps a gateway token to the session it was minted for.
type mcpSessionTokens struct {
	mu       sync.RWMutex
	toSess   map[string]string // token -> session id
	fromSess map[string]string // session id -> token (so a session's token can be revoked)
	pending  map[string]string // token -> placeholder, before the session id is known
}

func newMCPSessionTokens() *mcpSessionTokens {
	return &mcpSessionTokens{
		toSess:   map[string]string{},
		fromSess: map[string]string{},
		pending:  map[string]string{},
	}
}

// mint issues a token before the session exists. The provider assigns the real session id only after
// it starts, so the token is created first and bound a moment later.
func (t *mcpSessionTokens) mint() string {
	token := randToken() + randToken() // 128 bits
	t.mu.Lock()
	t.pending[token] = ""
	t.mu.Unlock()
	return token
}

// bind attaches a minted token to the session that ended up owning it.
func (t *mcpSessionTokens) bind(token, sessionID string) {
	if token == "" || sessionID == "" {
		return
	}
	t.mu.Lock()
	delete(t.pending, token)
	t.toSess[token] = sessionID
	t.fromSess[sessionID] = token
	t.mu.Unlock()
}

// session resolves a token. ok=false means the token is unknown or was never bound.
func (t *mcpSessionTokens) session(token string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	id, ok := t.toSess[token]
	return id, ok
}

// revoke drops a session's token when the session ends.
func (t *mcpSessionTokens) revoke(sessionID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	token := t.fromSess[sessionID]
	delete(t.fromSess, sessionID)
	delete(t.toSess, token)
	return token
}

// authorizeMCPTool is the gateway's Authorizer. It applies, in order: the session's MODE (a
// read-only session must not be able to write via an MCP server), then persisted approval RULES,
// then a real approval the user answers.
func (h *Hub) authorizeMCPTool(ctx context.Context, token, server, tool string, args json.RawMessage) error {
	// Present it to the user (and to the rule engine) as a qualified name, so a rule can target one
	// server's tool without colliding with a native tool of the same name.
	qualified := "mcp:" + server + ":" + tool
	ar := protocol.ApprovalRequest{
		ApprovalID: "mcpap_" + randToken(),
		Tool:       qualified,
		Detail:     tool + " (via the " + server + " MCP server)" + argSummary(args),
		// The arguments are what the tool will actually act on, so they must reach the guard and the
		// rule engine — not just the log. Carrying only the name meant an approval card said "write"
		// without saying WHAT, and the .git guard had nothing to inspect.
		Input: args,
	}

	// The guard runs FIRST, before any question of whose call this is.
	//
	// It used to sit after session attribution, which quietly made it optional: a call presenting the
	// machine-wide token resolves to no session and returned early, so it faced no mode gate, no
	// rules, no approval and no guard. The justification for that early return — anything that can
	// read a 0600 file in ~/.oculus can read ~/.oculus/key too, so it is game over already — is sound
	// against a local ATTACKER and unsound against the AGENT. The agent is not a trusted local user;
	// it is the party this whole system constrains, and it runs as that user with a shell. `cat
	// ~/.oculus/mcp-token` was a complete bypass of everything above.
	//
	// A .git write is refused because of what it IS, not because of who asked, so this check needs no
	// session and belongs ahead of the lookup.
	if g := guardApproval(ar); g.reason != "" {
		log.Printf("mcp: refused %s — %s", qualified, g.reason)
		return fmt.Errorf("refused: %s", g.reason)
	}

	sessionID, ok := h.mcpTokens.session(token)
	if !ok {
		// The machine-wide token (or an unbound one): no session context, so no rules can be applied
		// and no approval can be attributed. Allowed past the session policy because this is the path
		// the user's own tooling uses — but no longer past the guard above, and the gateway confines
		// it to loopback so a LAN caller cannot present it.
		return nil
	}
	m := h.managed(sessionID)
	if m == nil {
		return nil // the session ended mid-call; nothing to enforce against
	}
	ar.SessionID = sessionID

	if mode := m.sessionMode(); modeDeniesTool(mode, tool) {
		log.Printf("mcp: denied %s — session %s is in %s mode", qualified, sessionID, mode)
		return fmt.Errorf("this session is in %s mode, which is read-only", mode)
	}

	m.mu.Lock()
	projectID := m.meta.projectID
	execKind := m.meta.execKind
	m.mu.Unlock()
	if decision, matched := h.evaluateApproval(m.sess.Provider(), projectID, execKind, ar); matched {
		if decision == protocol.DecisionAllow {
			return nil
		}
		return fmt.Errorf("a standing rule denies %s", qualified)
	}

	return h.askForMCPApproval(ctx, m, ar)
}

// askForMCPApproval surfaces a real approval card and blocks until it's answered. Unlike a native
// tool approval — which the provider itself is blocking on — this one blocks an HTTP request, so it
// carries its own timeout.
func (h *Hub) askForMCPApproval(ctx context.Context, m *managedSession, ar protocol.ApprovalRequest) error {
	ar.SuggestedScopes = suggestScopes(ar, nil, "")
	answer := make(chan string, 1)

	h.mu.Lock()
	if h.mcpApprovals == nil {
		h.mcpApprovals = map[string]chan string{}
	}
	h.mcpApprovals[ar.ApprovalID] = answer
	h.approvals[ar.ApprovalID] = m
	if h.approvalReqs == nil {
		h.approvalReqs = map[string]pendingApproval{}
	}
	h.approvalReqs[ar.ApprovalID] = pendingApproval{req: ar, provider: m.sess.Provider()}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.mcpApprovals, ar.ApprovalID)
		delete(h.approvals, ar.ApprovalID)
		delete(h.approvalReqs, ar.ApprovalID)
		h.mu.Unlock()
	}()

	m.mu.Lock()
	m.pendingApprovals++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.pendingApprovals > 0 {
			m.pendingApprovals--
		}
		m.mu.Unlock()
	}()

	h.pushApproval(ar)
	if raw, err := encodeApprovalRequest(ar); err == nil {
		m.broadcast(raw)
	}

	select {
	case decision := <-answer:
		if decision == protocol.DecisionDeny {
			return fmt.Errorf("you denied %s", ar.Tool)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for approval of %s", ar.Tool)
	case <-time.After(mcpApprovalTimeout):
		return fmt.Errorf("nobody answered the approval for %s within %s", ar.Tool, mcpApprovalTimeout)
	}
}

// resolveMCPApproval delivers an answer to a waiting MCP call. Returns true when the approval id
// belonged to one, so the normal provider-Respond path is skipped.
func (h *Hub) resolveMCPApproval(approvalID, decision string) bool {
	h.mu.Lock()
	ch := h.mcpApprovals[approvalID]
	h.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- decision:
	default:
	}
	return true
}

// discard drops a token that was minted but whose session never came to exist.
func (t *mcpSessionTokens) discard(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, token)
	if sid, ok := t.toSess[token]; ok {
		delete(t.toSess, token)
		delete(t.fromSess, sid)
	}
}
