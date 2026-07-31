package hub

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// namedFakeSess is approvalFakeSess with a distinct id — the shared fake hardcodes one id, which
// would make two sessions in one test indistinguishable to a per-session sweep.
type namedFakeSess struct {
	approvalFakeSess
	id string
}

func (f *namedFakeSess) ID() string { return f.id }
func (f *namedFakeSess) Respond(_ context.Context, id, decision string) error {
	f.responded.Store(id + "|" + decision)
	return nil
}

// TestDeadSessionSweepsItsApprovals: a session dying with questions outstanding used to leak on
// three levels — hub map entries pinning the dead session forever, client cards whose Answer could
// only error "no such approval", and MCP calls blocked out their full 10-minute ceiling. The sweep
// runs on BOTH exit paths (user delete and unexpected stream end).
func TestDeadSessionSweepsItsApprovals(t *testing.T) {
	h := New()
	h.SetApprovalRulesPath(filepath.Join(t.TempDir(), "rules.json"))

	dead := &namedFakeSess{approvalFakeSess: approvalFakeSess{ch: make(chan agent.Event, 1)}, id: "dead-sess"}
	m := newManagedSession(h, dead, sessionMeta{})
	h.mu.Lock()
	h.sessions[dead.ID()] = m
	h.mu.Unlock()

	// Two pending approvals for the dying session, one for a survivor.
	h.recordApproval(protocol.ApprovalRequest{ApprovalID: "ap-1", SessionID: dead.ID(), Tool: "bash"}, m)
	h.recordApproval(protocol.ApprovalRequest{ApprovalID: "ap-2", SessionID: dead.ID(), Tool: "edit"}, m)
	survivor := &namedFakeSess{approvalFakeSess: approvalFakeSess{ch: make(chan agent.Event, 1)}, id: "other"}
	sm := newManagedSession(h, survivor, sessionMeta{})
	h.mu.Lock()
	h.sessions["other"] = sm
	h.mu.Unlock()
	h.recordApproval(protocol.ApprovalRequest{ApprovalID: "ap-other", SessionID: "other", Tool: "bash"}, sm)

	// An MCP call blocked on one of the dying session's approvals must unblock immediately.
	unblocked := make(chan string, 1)
	h.mu.Lock()
	h.mcpApprovals["ap-1"] = unblocked
	h.mu.Unlock()

	h.removeSession(dead.ID(), m)

	h.mu.Lock()
	_, has1 := h.approvals["ap-1"]
	_, has2 := h.approvals["ap-2"]
	_, hasReq1 := h.approvalReqs["ap-1"]
	_, hasOther := h.approvals["ap-other"]
	h.mu.Unlock()
	if has1 || has2 || hasReq1 {
		t.Fatal("the dead session's approvals must be swept from the hub maps")
	}
	if !hasOther {
		t.Fatal("a LIVING session's approvals must not be touched by another session's sweep")
	}
	select {
	case d := <-unblocked:
		if d != protocol.DecisionDeny {
			t.Errorf("a held MCP call must be denied (the tool never ran), got %q", d)
		}
	default:
		t.Fatal("a held MCP call must unblock when its session dies, not wait out its ceiling")
	}
}
