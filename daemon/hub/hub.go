// Package hub is the daemon core: it registers providers, and for each client
// connection routes protocol requests to sessions and forwards session events back
// over the encrypted transport.
package hub

import (
	"context"
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// DiscoverFunc autodetects active agent artifacts on the host (see daemon/discovery).
type DiscoverFunc func(context.Context) ([]protocol.Discovered, error)

// Hub owns providers and live sessions.
type Hub struct {
	mu        sync.Mutex
	providers map[string]agent.Provider
	sessions  map[string]agent.Session // sessionID -> session
	approvals map[string]agent.Session // approvalID -> owning session
	discover  DiscoverFunc
}

// New returns an empty Hub.
func New() *Hub {
	return &Hub{
		providers: map[string]agent.Provider{},
		sessions:  map[string]agent.Session{},
		approvals: map[string]agent.Session{},
	}
}

// Register adds a provider (keyed by Name()).
func (h *Hub) Register(p agent.Provider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers[p.Name()] = p
}

// SetDiscoverer installs the host-scan used to answer discover.list requests.
func (h *Hub) SetDiscoverer(f DiscoverFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.discover = f
}

// Serve handles one client connection until it closes or errors.
func (h *Hub) Serve(ctx context.Context, conn *transport.Conn) error {
	for {
		raw, err := conn.Recv()
		if err != nil {
			return err
		}
		env, err := protocol.Decode(raw)
		if err != nil {
			h.sendErr(conn, "", "bad message")
			continue
		}
		h.dispatch(ctx, conn, env)
	}
}

func (h *Hub) dispatch(ctx context.Context, conn *transport.Conn, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeSessionCreate:
		var req protocol.SessionCreate
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad session.create")
			return
		}
		h.mu.Lock()
		p := h.providers[req.Provider]
		h.mu.Unlock()
		if p == nil {
			h.sendErr(conn, env.ID, "unknown provider: "+req.Provider)
			return
		}
		sess, err := p.Create(ctx, req.Cwd, req.Prompt)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.mu.Lock()
		h.sessions[sess.ID()] = sess
		h.mu.Unlock()
		go h.forward(conn, sess)
		h.sendOK(conn, env.ID, protocol.Session{ID: sess.ID(), Provider: sess.Provider(), Status: protocol.StatusRunning})

	case protocol.TypeSessionList:
		h.mu.Lock()
		list := make([]protocol.Session, 0, len(h.sessions))
		for _, s := range h.sessions {
			list = append(list, protocol.Session{ID: s.ID(), Provider: s.Provider(), Status: protocol.StatusRunning})
		}
		h.mu.Unlock()
		h.sendOK(conn, env.ID, protocol.SessionList{Sessions: list})

	case protocol.TypeSessionPrompt:
		var req protocol.SessionPrompt
		_ = env.Unmarshal(&req)
		s := h.session(req.SessionID)
		if s == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		if err := s.Prompt(ctx, req.Text); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeApprovalRespond:
		var req protocol.ApprovalRespond
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		s := h.approvals[req.ApprovalID]
		delete(h.approvals, req.ApprovalID)
		h.mu.Unlock()
		if s == nil {
			h.sendErr(conn, env.ID, "no such approval")
			return
		}
		if err := s.Respond(ctx, req.ApprovalID, req.Decision); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeDiscover:
		h.mu.Lock()
		f := h.discover
		h.mu.Unlock()
		items := []protocol.Discovered{}
		if f != nil {
			got, err := f(ctx)
			if err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			items = got
		}
		h.sendOK(conn, env.ID, protocol.DiscoverList{Items: items})

	case protocol.TypeSessionStop:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		if s := h.session(req.SessionID); s != nil {
			_ = s.Stop(ctx)
		}
		h.sendOK(conn, env.ID, nil)

	default:
		h.sendErr(conn, env.ID, "unknown type: "+env.Type)
	}
}

// forward pumps a session's events to the client, recording approval ownership.
func (h *Hub) forward(conn *transport.Conn, sess agent.Session) {
	for ev := range sess.Events() {
		if ev.Type == protocol.TypeApprovalRequest {
			if ar, ok := ev.Payload.(protocol.ApprovalRequest); ok {
				h.mu.Lock()
				h.approvals[ar.ApprovalID] = sess
				h.mu.Unlock()
			}
		}
		raw, err := ev.Encode()
		if err != nil {
			continue
		}
		if err := conn.Send(raw); err != nil {
			return
		}
	}
	h.mu.Lock()
	delete(h.sessions, sess.ID())
	h.mu.Unlock()
}

func (h *Hub) session(id string) agent.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

func (h *Hub) sendOK(conn *transport.Conn, id string, payload any) {
	if raw, err := protocol.Encode(id, protocol.TypeOK, payload); err == nil {
		_ = conn.Send(raw)
	}
}

func (h *Hub) sendErr(conn *transport.Conn, id, msg string) {
	if raw, err := protocol.Encode(id, protocol.TypeError, protocol.Error{Message: msg}); err == nil {
		_ = conn.Send(raw)
	}
}
