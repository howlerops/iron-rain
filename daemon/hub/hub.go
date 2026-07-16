// Package hub is the daemon core: it registers providers, and for each client
// connection routes protocol requests to sessions and forwards session events back
// over the encrypted transport.
package hub

import (
	"context"
	"log"
	"sync"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
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

	notifier   push.Notifier // optional: push actionable approvals to a device
	pushTokens []string      // registered device tokens
	attach     AttacherFactory
	clients    map[*transport.Conn]bool // all connected clients (for broadcasts)
}

// AttacherFactory returns an Attacher for a discovered session (by provider + URL),
// or nil if that provider/URL can't be attached.
type AttacherFactory func(provider, url string) agent.Attacher

// New returns an empty Hub.
func New() *Hub {
	return &Hub{
		providers: map[string]agent.Provider{},
		sessions:  map[string]agent.Session{},
		approvals: map[string]agent.Session{},
		clients:   map[*transport.Conn]bool{},
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

// SetNotifier installs a push Notifier; approval requests are then pushed to every
// registered device token (see RegisterDevice). A nil Notifier disables push.
func (h *Hub) SetNotifier(n push.Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifier = n
}

// SetAttacherFactory installs the factory used to attach to discovered sessions.
func (h *Hub) SetAttacherFactory(f AttacherFactory) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attach = f
}

// RegisterDevice adds a device token to receive approval pushes.
func (h *Hub) RegisterDevice(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.pushTokens {
		if t == token {
			return
		}
	}
	h.pushTokens = append(h.pushTokens, token)
}

// pushApproval delivers an approval to every registered device (best-effort, async).
func (h *Hub) pushApproval(ar protocol.ApprovalRequest) {
	h.mu.Lock()
	n := h.notifier
	tokens := append([]string(nil), h.pushTokens...)
	h.mu.Unlock()
	if n == nil || len(tokens) == 0 {
		return
	}
	notif := push.Notification{
		Title:    "Approve " + ar.Tool,
		Body:     "Tap to review in Oculus",
		Category: "APPROVAL",
		ThreadID: ar.SessionID,
		Custom:   map[string]any{"approval_id": ar.ApprovalID, "session_id": ar.SessionID},
	}
	log.Printf("hub: pushing approval %s (tool %q) to %d device(s)", ar.ApprovalID, ar.Tool, len(tokens))
	for _, t := range tokens {
		go func(token string) {
			if err := n.Notify(context.Background(), token, notif); err != nil {
				log.Printf("hub: push to %s… failed: %v", safePrefix(token), err)
			} else {
				log.Printf("hub: push to %s… delivered to APNs", safePrefix(token))
			}
		}(t)
	}
}

func safePrefix(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// Serve handles one client connection until it closes or errors.
func (h *Hub) Serve(ctx context.Context, conn *transport.Conn) error {
	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
	}()
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

// broadcast sends an event to every connected client (used for cross-device sync).
func (h *Hub) broadcast(typ string, payload any) {
	raw, err := protocol.Encode("", typ, payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	conns := make([]*transport.Conn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.Send(raw)
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
		// Tell every client this approval was answered, so its card clears everywhere.
		h.broadcast(protocol.TypeApprovalResolved, protocol.ApprovalResolved{ApprovalID: req.ApprovalID, Decision: req.Decision})

	case protocol.TypeSessionAttach:
		var req protocol.SessionAttach
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad session.attach")
			return
		}
		h.mu.Lock()
		factory := h.attach
		registered := h.providers[req.Provider]
		h.mu.Unlock()
		var att agent.Attacher
		if factory != nil {
			att = factory(req.Provider, req.URL)
		}
		if att == nil {
			att, _ = registered.(agent.Attacher)
		}
		if att == nil {
			h.sendErr(conn, env.ID, "provider cannot attach: "+req.Provider)
			return
		}
		sess, err := att.Attach(ctx, req.SessionID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.mu.Lock()
		h.sessions[sess.ID()] = sess
		h.mu.Unlock()
		go h.forward(conn, sess)
		h.sendOK(conn, env.ID, protocol.Session{ID: sess.ID(), Provider: sess.Provider(), Status: protocol.StatusRunning})

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

	case protocol.TypeDeviceRegister:
		var req protocol.DeviceRegister
		if err := env.Unmarshal(&req); err != nil || req.Token == "" {
			h.sendErr(conn, env.ID, "bad device.register")
			return
		}
		h.RegisterDevice(req.Token)
		log.Printf("hub: device registered for push (token %s…, %d chars)", safePrefix(req.Token), len(req.Token))
		h.sendOK(conn, env.ID, nil)

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
				h.pushApproval(ar) // actionable lock-screen push (no-op if unconfigured)
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
