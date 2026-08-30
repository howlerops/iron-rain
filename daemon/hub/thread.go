package hub

import (
	"context"
	"fmt"
	"log"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// Going back to an earlier point in a conversation.
//
// Every provider means something slightly different by it, and the differences are not cosmetic:
// opencode's fork creates a NEW session while pi's rebinds the one you are in; opencode can rewind
// in place and undo that rewind, pi cannot rewind at all over rpc. The hub does not paper over any
// of that — it reports what happened so the client can act on the truth rather than on an average.

// threadOps returns the session's thread implementation, or an error naming what is missing.
func threadOps(m *managedSession) (agent.ThreadOps, error) {
	ops, ok := m.sess.(agent.ThreadOps)
	if !ok {
		return nil, fmt.Errorf("%s sessions cannot branch their history", m.sess.Provider())
	}
	return ops, nil
}

// handleThreadTree answers with the points this conversation can be forked or rewound to.
func (h *Hub) handleThreadTree(ctx context.Context, conn *transport.Conn, envID string, req protocol.SessionRef) {
	m := h.managed(req.SessionID)
	if m == nil {
		h.sendErr(conn, envID, "no such session")
		return
	}
	ops, err := threadOps(m)
	if err != nil {
		h.sendErr(conn, envID, err.Error())
		return
	}
	nodes, err := ops.ThreadTree(ctx)
	if err != nil {
		h.sendErr(conn, envID, err.Error())
		return
	}
	h.sendOK(conn, envID, protocol.ThreadTreeResult{SessionID: req.SessionID, Nodes: nodes})
}

// handleThreadFork branches the conversation at a node.
func (h *Hub) handleThreadFork(ctx context.Context, conn *transport.Conn, envID string, req protocol.ThreadRef) {
	m := h.managed(req.SessionID)
	if m == nil {
		h.sendErr(conn, envID, "no such session")
		return
	}
	ops, err := threadOps(m)
	if err != nil {
		h.sendErr(conn, envID, err.Error())
		return
	}
	newID, err := ops.ThreadFork(ctx, req.NodeID)
	if err != nil {
		h.sendErr(conn, envID, err.Error())
		return
	}
	// A fork that produced a DIFFERENT provider-side session is a session the daemon does not manage
	// yet. Adopt it before answering, or the client is handed an id it cannot open — which would look
	// like the fork failing when it in fact succeeded.
	isNew := newID != "" && newID != m.sess.ID()
	if isNew {
		if err := h.adoptForkedSession(ctx, m, newID); err != nil {
			log.Printf("thread: forked %s → %s but could not attach: %v", req.SessionID, newID, err)
			h.sendErr(conn, envID, "the fork was created but could not be opened: "+err.Error())
			return
		}
	}
	log.Printf("thread: forked session %s at %s → %s (new=%v)", req.SessionID, req.NodeID, newID, isNew)
	h.sendOK(conn, envID, protocol.ThreadForkResult{SessionID: newID, New: isNew})
	h.broadcastSessionList()
}

// handleThreadRewind moves a session back to an earlier node.
func (h *Hub) handleThreadRewind(ctx context.Context, conn *transport.Conn, envID string, req protocol.ThreadRef) {
	m := h.managed(req.SessionID)
	if m == nil {
		h.sendErr(conn, envID, "no such session")
		return
	}
	ops, err := threadOps(m)
	if err != nil {
		h.sendErr(conn, envID, err.Error())
		return
	}
	if err := ops.ThreadRewind(ctx, req.NodeID); err != nil {
		h.sendErr(conn, envID, err.Error())
		return
	}
	log.Printf("thread: rewound session %s to %s", req.SessionID, req.NodeID)
	h.sendOK(conn, envID, nil)
	// The transcript the client is holding now describes a future that no longer exists. Facts carry
	// the mode and the rest of the status bar; the session list carries the row.
	h.broadcastFacts(m)
	h.broadcastSessionList()
}

// adoptForkedSession attaches the daemon to a session a fork just created, reusing the parent's
// project/cwd so the new one lands in the same place.
func (h *Hub) adoptForkedSession(ctx context.Context, parent *managedSession, newID string) error {
	name := parent.sess.Provider()
	h.mu.Lock()
	prov := h.providers[name]
	h.mu.Unlock()
	if prov == nil {
		return fmt.Errorf("provider %s is no longer registered", name)
	}
	att, ok := prov.(agent.Attacher)
	if !ok {
		return fmt.Errorf("provider %s cannot attach to an existing session", name)
	}
	parent.mu.Lock()
	meta := parent.meta
	parent.mu.Unlock()

	sess, err := att.Attach(ctx, newID, meta.cwd)
	if err != nil {
		return err
	}
	m := newManagedSession(h, sess, meta)
	h.mu.Lock()
	h.sessions[sess.ID()] = m
	h.mu.Unlock()
	h.persistSession(m)
	return nil
}
