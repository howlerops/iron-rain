package hub

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Persistent "Always allow" rules: answering an approval with ALWAYS used to persist only inside
// that one provider session — every new session re-asked for the same tools, which made approvals
// feel broken ("we should only ever need to get permissions once"). A rule is provider|tool, stored
// in ~/.oculus/approval-rules.json; matching approvals are auto-allowed daemon-side and never
// surfaced.

func approvalRuleKey(provider, tool string) string { return provider + "|" + tool }

// SetApprovalRulesPath records where rules persist and loads any saved ones.
func (h *Hub) SetApprovalRulesPath(path string) {
	h.mu.Lock()
	h.approvalRulesPath = path
	h.approvalAllow = map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		var keys []string
		if json.Unmarshal(data, &keys) == nil {
			for _, k := range keys {
				h.approvalAllow[k] = true
			}
		}
	}
	h.mu.Unlock()
}

// rememberApprovalRule persists an ALWAYS decision so every future session auto-allows this
// provider+tool.
func (h *Hub) rememberApprovalRule(provider, tool string) {
	if provider == "" || tool == "" {
		return
	}
	h.mu.Lock()
	if h.approvalAllow == nil {
		h.approvalAllow = map[string]bool{}
	}
	h.approvalAllow[approvalRuleKey(provider, tool)] = true
	keys := make([]string, 0, len(h.approvalAllow))
	for k := range h.approvalAllow {
		keys = append(keys, k)
	}
	path := h.approvalRulesPath
	h.mu.Unlock()
	if path != "" {
		if data, err := json.Marshal(keys); err == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	}
	log.Printf("approvals: ALWAYS rule saved — %s/%s auto-allowed for every session", provider, tool)
}

// autoAllowApproval answers a request immediately (allow) when a persisted ALWAYS rule matches,
// returning true so the caller drops the request instead of surfacing it.
func (h *Hub) autoAllowApproval(m *managedSession, ar protocol.ApprovalRequest) bool {
	h.mu.Lock()
	ok := h.approvalAllow[approvalRuleKey(m.sess.Provider(), ar.Tool)]
	h.mu.Unlock()
	if !ok {
		return false
	}
	log.Printf("approvals: auto-allowed %s (%s) via persisted ALWAYS rule", ar.Tool, m.sess.Provider())
	go func() { _ = m.sess.Respond(context.Background(), ar.ApprovalID, protocol.DecisionAllow) }()
	return true
}
