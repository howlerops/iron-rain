package hub

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/howlerops/oculus/daemon/fsaccess"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Persistent approval rules. Answering an approval with ALWAYS used to persist only inside that one
// provider session — every new session re-asked for the same tools, which made approvals feel broken
// ("we should only ever need to get permissions once"). Rules live in ~/.oculus/approval-rules.json
// and are matched daemon-side, so they apply to EVERY harness uniformly (opencode, claude-code, pi,
// BYO CLI) even though only some of those have a native "always" concept.
//
// A rule is deliberately more than provider+tool: "always allow bash" is far too broad when the tool
// argument is the thing that matters. A rule may additionally scope to a command/URL glob (Pattern),
// a filesystem subtree (PathPrefix), or one project (ProjectID). Empty fields mean "any".
//
// Precedence, highest first (mirrors the model Zed's tool_permissions settled on, which is the one
// users already understand): explicit deny > explicit allow > ask. Within a class the first matching
// rule in file order wins, so a user can put a narrow exception above a broad rule.

// ApprovalRule is one persisted decision. Zero-valued fields match anything.
type ApprovalRule struct {
	Provider   string `json:"provider,omitempty"`    // "" = any harness
	Tool       string `json:"tool,omitempty"`        // "" = any tool
	Pattern    string `json:"pattern,omitempty"`     // glob over the request's Detail (command / URL / path)
	PathPrefix string `json:"path_prefix,omitempty"` // filesystem subtree the request must stay inside
	ProjectID  string `json:"project_id,omitempty"`  // "" = global, else only this project's sessions
	Action     string `json:"action"`                // allow | deny
}

// approvalRuleKey is the legacy provider|tool identity, kept because the on-disk v1 format was a bare
// []string of these and old files must keep working.
func approvalRuleKey(provider, tool string) string { return provider + "|" + tool }

// SetApprovalRulesPath records where rules persist and loads any saved ones, migrating the legacy
// []string format ("provider|tool" allow-only) in place.
func (h *Hub) SetApprovalRulesPath(path string) {
	h.mu.Lock()
	h.approvalRulesPath = path
	h.approvalRules = nil
	data, err := os.ReadFile(path)
	h.mu.Unlock()
	if err != nil {
		return
	}
	rules, migrated := parseApprovalRules(data)
	h.mu.Lock()
	h.approvalRules = rules
	h.mu.Unlock()
	if migrated && len(rules) > 0 {
		log.Printf("approvals: migrated %d legacy provider|tool rule(s) to the scoped format", len(rules))
		h.saveApprovalRules()
	}
}

// parseApprovalRules decodes either the current []ApprovalRule format or the legacy []string one.
// The second return reports whether a legacy file was migrated.
func parseApprovalRules(data []byte) ([]ApprovalRule, bool) {
	var rules []ApprovalRule
	if err := json.Unmarshal(data, &rules); err == nil {
		out := rules[:0]
		for _, r := range rules {
			if r.Action == "" {
				r.Action = protocol.DecisionAllow // tolerate a hand-edited file that omits it
			}
			if r.Action != protocol.DecisionAllow && r.Action != "deny" {
				continue
			}
			// Refuse a rule with NO scope at all: as an allow it would silently approve every tool on
			// every harness forever. Such a rule can only arrive from a corrupt/hand-broken file.
			if r.isUnscoped() {
				log.Printf("approvals: ignoring an unscoped %q rule (it would match every request)", r.Action)
				continue
			}
			out = append(out, r)
		}
		return out, false
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, false
	}
	// Start clean: a failed decode above can leave `rules` holding zero-valued elements (encoding/json
	// grows the slice before it hits the type error), which would otherwise be written back out as
	// empty catch-all rules — i.e. a migration that silently allows everything.
	rules = nil
	for _, k := range keys {
		provider, tool, ok := strings.Cut(k, "|")
		if !ok || tool == "" {
			continue
		}
		rules = append(rules, ApprovalRule{Provider: provider, Tool: tool, Action: protocol.DecisionAllow})
	}
	return rules, true
}

// saveApprovalRules writes the rule list. It snapshots under the lock and writes UNLOCKED, so disk
// I/O never blocks the hub.
func (h *Hub) saveApprovalRules() {
	h.mu.Lock()
	path := h.approvalRulesPath
	snapshot := make([]ApprovalRule, len(h.approvalRules))
	copy(snapshot, h.approvalRules)
	h.mu.Unlock()
	if path == "" {
		return
	}
	if data, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// addApprovalRule appends a rule (de-duplicating an identical one) and persists.
func (h *Hub) addApprovalRule(r ApprovalRule) {
	if r.Action == "" {
		r.Action = protocol.DecisionAllow
	}
	if r.isUnscoped() {
		log.Printf("approvals: refusing to save an unscoped %q rule", r.Action)
		return
	}
	h.mu.Lock()
	for _, existing := range h.approvalRules {
		if existing == r {
			h.mu.Unlock()
			return
		}
	}
	h.approvalRules = append(h.approvalRules, r)
	h.mu.Unlock()
	h.saveApprovalRules()
	log.Printf("approvals: rule saved — %s", r.describe())
}

// rememberApprovalRule persists a plain provider+tool ALWAYS (the un-scoped answer).
func (h *Hub) rememberApprovalRule(provider, tool string) {
	if provider == "" || tool == "" {
		return
	}
	h.addApprovalRule(ApprovalRule{Provider: provider, Tool: tool, Action: protocol.DecisionAllow})
}

// isUnscoped reports whether a rule constrains nothing — it would match every request from every
// harness. Never load or persist one.
func (r ApprovalRule) isUnscoped() bool {
	return r.Provider == "" && r.Tool == "" && r.Pattern == "" && r.PathPrefix == "" && r.ProjectID == ""
}

// describe renders a rule for logs and the rules UI.
func (r ApprovalRule) describe() string {
	var b strings.Builder
	b.WriteString(r.Action)
	b.WriteString(" ")
	if r.Tool == "" {
		b.WriteString("any tool")
	} else {
		b.WriteString(r.Tool)
	}
	if r.Pattern != "" {
		b.WriteString(" matching " + r.Pattern)
	}
	if r.PathPrefix != "" {
		b.WriteString(" under " + r.PathPrefix)
	}
	if r.Provider != "" {
		b.WriteString(" on " + r.Provider)
	}
	if r.ProjectID != "" {
		b.WriteString(" in project " + r.ProjectID)
	}
	return b.String()
}

// matches reports whether this rule covers a specific approval request.
func (r ApprovalRule) matches(provider, projectID string, ar protocol.ApprovalRequest) bool {
	if r.Provider != "" && r.Provider != provider {
		return false
	}
	if r.Tool != "" && r.Tool != ar.Tool {
		return false
	}
	if r.ProjectID != "" && r.ProjectID != projectID {
		return false
	}
	if r.Pattern != "" && !globMatch(r.Pattern, ar.Detail) {
		return false
	}
	if r.PathPrefix != "" && !withinPrefix(r.PathPrefix, ar.Detail) {
		return false
	}
	return true
}

// evaluateApproval resolves a request against the rule set. It returns the decision to apply and
// whether any rule matched. Deny is checked before allow so a narrow "never touch .env" beats a broad
// "always allow edit", regardless of file order.
func (h *Hub) evaluateApproval(provider, projectID string, ar protocol.ApprovalRequest) (string, bool) {
	h.mu.Lock()
	rules := make([]ApprovalRule, len(h.approvalRules))
	copy(rules, h.approvalRules)
	h.mu.Unlock()
	for _, r := range rules {
		if r.Action == "deny" && r.matches(provider, projectID, ar) {
			return "deny", true
		}
	}
	for _, r := range rules {
		if r.Action == protocol.DecisionAllow && r.matches(provider, projectID, ar) {
			return protocol.DecisionAllow, true
		}
	}
	return "", false
}

// autoAllowApproval answers a request immediately when a persisted rule matches, returning true so
// the caller drops it instead of surfacing it. An auto-DENY is answered the same way — silently —
// because a rule the user wrote is a standing answer, not a question to re-ask.
func (h *Hub) autoAllowApproval(m *managedSession, ar protocol.ApprovalRequest) bool {
	m.mu.Lock()
	projectID := m.meta.projectID
	m.mu.Unlock()
	decision, ok := h.evaluateApproval(m.sess.Provider(), projectID, ar)
	if !ok {
		return false
	}
	log.Printf("approvals: auto-%sed %s (%s) via persisted rule", decision, ar.Tool, m.sess.Provider())
	go func() { _ = m.sess.Respond(context.Background(), ar.ApprovalID, decision) }()
	return true
}

// globMatch reports whether pattern matches s. Unlike filepath.Match, '*' spans ANY characters
// including '/' — these patterns run against shell commands and URLs ("git *", "*://internal.example/*"),
// where path-segment semantics would surprise people. '?' matches exactly one character.
func globMatch(pattern, s string) bool {
	// Iterative two-pointer with backtracking: linear in practice, no regex compile per call.
	var star, mark int = -1, 0
	i, j := 0, 0
	for i < len(s) {
		switch {
		case j < len(pattern) && (pattern[j] == '?' || pattern[j] == s[i]):
			i++
			j++
		case j < len(pattern) && pattern[j] == '*':
			star = j
			mark = i
			j++
		case star >= 0:
			j = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for j < len(pattern) && pattern[j] == '*' {
		j++
	}
	return j == len(pattern)
}

// withinPrefix reports whether detail names a path inside prefix. It is intentionally conservative:
// both sides are normalized through fsaccess (~ expansion, symlink resolution on the existing
// ancestor) — so a rule can't be dodged by a symlink — and a match requires a SEPARATOR boundary, so
// "/repo-secrets" is never treated as inside "/repo". A Detail that isn't an absolute path (a bare
// command like "npm test") never matches a subtree rule.
func withinPrefix(prefix, detail string) bool {
	p := fsaccess.NormalizePath(prefix)
	d := fsaccess.NormalizePath(detail)
	if p == "" || d == "" {
		return false
	}
	if d == p {
		return true
	}
	return strings.HasPrefix(d, strings.TrimSuffix(p, string(os.PathSeparator))+string(os.PathSeparator))
}

// ruleFromDecision turns an answered approval into the rule to persist. A nil scope reproduces the
// original behavior (this provider + this tool, everywhere), so an older client keeps working.
func ruleFromDecision(p pendingApproval, scope *protocol.ApprovalScope) ApprovalRule {
	r := ApprovalRule{Provider: p.provider, Tool: p.req.Tool, Action: protocol.DecisionAllow}
	if scope == nil {
		return r
	}
	switch scope.Kind {
	case "pattern":
		r.Pattern = scope.Value
	case "path":
		r.PathPrefix = scope.Value
	case "project":
		r.ProjectID = p.projectID
	}
	return r
}

// suggestScopes proposes the ALWAYS scopes for one request, narrowest first, so the client's menu is
// generated from daemon-side knowledge instead of the app guessing at command syntax.
//
// The suggestions are deliberately conservative: a command becomes a first-token glob ("git *"), a
// path becomes its PARENT directory (not the file, which would be useless, and not the repo root,
// which would be too broad). Anything we can't characterize falls back to the plain tool rule.
func suggestScopes(ar protocol.ApprovalRequest, providerPatterns []string, projectID string) []protocol.ApprovalScope {
	var out []protocol.ApprovalScope
	// A harness that already tells us the matching patterns (opencode does) knows best — use it.
	for _, p := range providerPatterns {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, protocol.ApprovalScope{Kind: "pattern", Value: p, Label: `Always allow "` + p + `"`})
		}
	}
	detail := strings.TrimSpace(ar.Detail)
	if len(out) == 0 && detail != "" {
		if dir := commandPrefixGlob(detail); dir != "" {
			out = append(out, protocol.ApprovalScope{Kind: "pattern", Value: dir, Label: `Always allow "` + dir + `"`})
		}
	}
	if p := fsaccess.NormalizePath(detail); p != "" {
		if parent := filepath.Dir(p); parent != "" && parent != string(os.PathSeparator) {
			out = append(out, protocol.ApprovalScope{Kind: "path", Value: parent, Label: "Always allow in " + filepath.Base(parent) + "/"})
		}
	}
	out = append(out, protocol.ApprovalScope{Kind: "tool", Label: "Always allow " + ar.Tool})
	if projectID != "" {
		out = append(out, protocol.ApprovalScope{Kind: "project", Label: "Always allow " + ar.Tool + " in this project"})
	}
	return out
}

// commandPrefixGlob renders a shell command as a conservative glob over its leading word(s):
// "git commit -m x" -> "git *". A subcommand is kept when the leading word is a multiplexer whose
// subcommands differ wildly in risk ("git push" is not "git status"), because a bare "git *" rule
// would silently cover far more than the user just approved.
func commandPrefixGlob(detail string) string {
	fields := strings.Fields(detail)
	if len(fields) == 0 {
		return ""
	}
	// Anything with shell metacharacters isn't safely generalizable — a rule built from it could
	// match a very different command later.
	if strings.ContainsAny(detail, "|&;><$`\n") {
		return ""
	}
	head := filepath.Base(fields[0])
	multiplexers := map[string]bool{"git": true, "npm": true, "pnpm": true, "yarn": true, "cargo": true, "go": true, "docker": true, "kubectl": true, "brew": true, "gh": true}
	if multiplexers[head] && len(fields) > 1 && !strings.HasPrefix(fields[1], "-") {
		return head + " " + fields[1] + " *"
	}
	if len(fields) == 1 {
		return head
	}
	return head + " *"
}
