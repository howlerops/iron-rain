package hub

import (
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// managedSession is a hub-owned agent session shared by every subscribed client.
// A single run() goroutine reads the provider's event stream once, records it to a
// transcript (so late joiners can be caught up), and broadcasts each event to all
// current subscribers. This is the single-session-broadcast model: one provider
// subscription, many client observers — the daemon is the fan-out point.
type managedSession struct {
	hub  *Hub
	sess agent.Session
	meta sessionMeta // grouping info (project/cwd/worktree) for session.list + create

	mu         sync.Mutex
	subs       map[*transport.Conn]bool
	transcript [][]byte // encoded protocol events, replayed to new subscribers
}

// sessionMeta is where a session runs, so clients can group the sidebar.
type sessionMeta struct {
	projectID     string
	cwd           string
	workspaceName string
	branch        string
}

func newManagedSession(h *Hub, sess agent.Session, meta sessionMeta) *managedSession {
	return &managedSession{hub: h, sess: sess, meta: meta, subs: map[*transport.Conn]bool{}}
}

// info renders the session's identity + grouping metadata for the wire.
func (m *managedSession) info() protocol.Session {
	return protocol.Session{
		ID:            m.sess.ID(),
		Provider:      m.sess.Provider(),
		Status:        protocol.StatusRunning,
		ProjectID:     m.meta.projectID,
		Cwd:           m.meta.cwd,
		WorkspaceName: m.meta.workspaceName,
		Branch:        m.meta.branch,
	}
}

// subscribe adds a client and replays the transcript so it sees the whole session.
// A snapshot is taken under the lock; the replay is sent outside it. Events that
// arrive during the (brief) replay window reach the client via broadcast instead.
func (m *managedSession) subscribe(conn *transport.Conn) {
	m.mu.Lock()
	m.subs[conn] = true
	replay := append([][]byte(nil), m.transcript...)
	m.mu.Unlock()
	for _, raw := range replay {
		if conn.Send(raw) != nil {
			return
		}
	}
}

func (m *managedSession) unsubscribe(conn *transport.Conn) {
	m.mu.Lock()
	delete(m.subs, conn)
	m.mu.Unlock()
}

// broadcast records the event and sends it to every current subscriber.
func (m *managedSession) broadcast(raw []byte) {
	m.mu.Lock()
	m.transcript = append(m.transcript, raw)
	conns := make([]*transport.Conn, 0, len(m.subs))
	for c := range m.subs {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.Send(raw)
	}
}

// run pumps the session's events until it ends: records approval ownership + pushes,
// then broadcasts every event to all subscribers.
func (m *managedSession) run() {
	for ev := range m.sess.Events() {
		if ev.Type == protocol.TypeApprovalRequest {
			if ar, ok := ev.Payload.(protocol.ApprovalRequest); ok {
				m.hub.recordApproval(ar.ApprovalID, m)
				m.hub.pushApproval(ar)
			}
		}
		raw, err := ev.Encode()
		if err != nil {
			continue
		}
		m.broadcast(raw)
	}
	m.hub.removeSession(m.sess.ID())
}
