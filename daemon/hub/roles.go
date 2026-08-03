package hub

import (
	"log"
	"strings"
	"sync"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// Roles: who may STEER an agent, as opposed to who may watch it.
//
// The design is taken from what already works elsewhere and from one failure worth not repeating.
// Zed's collaboration model — guests read-only by default, with an explicit, revocable write grant —
// is the shape users already understand. The failure to avoid is Cursor's: a cloud agent session runs
// with the initiating user's credentials while the prompt surface is NOT bound to that user, so a
// teammate can steer a session that acts as someone else, invisibly.
//
// So: the session OWNER is whose machine and credentials the agent actually acts with, and it is
// always visible. Everyone else is an observer until the owner grants steer, and that grant is
// revocable. Approvals are owner-only — a steerer can ask the agent to do something, but only the
// person whose credentials are at stake can authorize a destructive tool.
//
// This is enforced in the DAEMON, at the same choke point as modes and approval rules, because the
// client cannot be trusted to enforce a permission it also renders.

const (
	// RoleOwner may do everything, including answer approvals. The owner is the daemon's local user.
	RoleOwner = "owner"
	// RoleSteerer may prompt and interrupt, but not answer approvals.
	RoleSteerer = "steerer"
	// RoleObserver may only watch.
	RoleObserver = "observer"
)

// roleRegistry tracks each connection's role. It is separate from hubClient so role checks never
// need the hub lock (they happen on every prompt).
type roleRegistry struct {
	mu sync.RWMutex
	// byConn is the authoritative role per live connection.
	byConn map[*transport.Conn]string
	// enabled gates the whole feature. Until someone actually shares a session, every connection is
	// the owner — a single-user setup must not acquire permission friction it never asked for.
	enabled bool
}

func newRoleRegistry() *roleRegistry {
	return &roleRegistry{byConn: map[*transport.Conn]string{}}
}

// SetEnabled turns role enforcement on or off.
func (r *roleRegistry) SetEnabled(on bool) {
	r.mu.Lock()
	r.enabled = on
	r.mu.Unlock()
}

func (r *roleRegistry) isEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled
}

// role returns a connection's role. With enforcement off, or for a connection we've never seen,
// the answer is owner — failing OPEN here is correct because the default deployment is one person
// on their own machine, and locking them out of their own agent would be absurd.
func (r *roleRegistry) role(conn *transport.Conn) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled {
		return RoleOwner
	}
	if role, ok := r.byConn[conn]; ok && role != "" {
		return role
	}
	return RoleObserver // enforcement on + unknown connection = watch only, until granted
}

// setRole assigns a role to a connection.
func (r *roleRegistry) setRole(conn *transport.Conn, role string) {
	r.mu.Lock()
	r.byConn[conn] = role
	r.mu.Unlock()
}

// forget drops a disconnected connection.
func (r *roleRegistry) forget(conn *transport.Conn) {
	r.mu.Lock()
	delete(r.byConn, conn)
	r.mu.Unlock()
}

// capability is what an action requires.
type capability int

const (
	capWatch capability = iota
	capSteer
	capApprove
	capOwner
)

// allows reports whether a role carries a capability.
func roleAllows(role string, c capability) bool {
	switch c {
	case capWatch:
		return true // every connected client may watch
	case capSteer:
		return role == RoleOwner || role == RoleSteerer
	case capApprove:
		return role == RoleOwner
	case capOwner:
		return role == RoleOwner
	}
	return false
}

// requireCapability gates one request. It returns true when the caller may proceed; otherwise it has
// already sent the error and the caller must return.
func (h *Hub) requireCapability(conn *transport.Conn, envID string, c capability, what string) bool {
	role := h.roles.role(conn)
	if roleAllows(role, c) {
		return true
	}
	who := h.clientName(conn)
	if who == "" {
		who = "an unidentified client"
	}
	log.Printf("roles: DENIED %s to %s (role %s)", what, who, role)
	switch c {
	case capApprove:
		h.sendErr(conn, envID, "Only the session owner can answer approvals.")
	case capOwner:
		h.sendErr(conn, envID, "Only the session owner can change daemon settings.")
	default:
		h.sendErr(conn, envID, "You're watching this session. Ask the owner for permission to steer.")
	}
	return false
}

// SetRolesEnabled turns multi-user enforcement on. Off by default: a solo user must never acquire
// permission friction they didn't ask for.
func (h *Hub) SetRolesEnabled(on bool) {
	h.roles.SetEnabled(on)
	log.Printf("roles: multi-user enforcement %s", map[bool]string{true: "ENABLED", false: "disabled"}[on])
}

// grantRole assigns a role to the connection whose declared name matches target.
func (h *Hub) grantRole(target, role string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	h.mu.Lock()
	var match *transport.Conn
	for conn, c := range h.clients {
		if strings.EqualFold(c.displayName(), target) {
			match = conn
			break
		}
	}
	h.mu.Unlock()
	if match == nil {
		return false
	}
	h.roles.setRole(match, role)
	log.Printf("roles: %s is now a %s", target, role)
	h.broadcastParticipants()
	return true
}

// participants renders who is connected and what they may do.
func (h *Hub) participants() protocol.ParticipantList {
	h.mu.Lock()
	type entry struct {
		name string
		conn *transport.Conn
	}
	entries := make([]entry, 0, len(h.clients))
	for conn, c := range h.clients {
		entries = append(entries, entry{name: c.displayName(), conn: conn})
	}
	h.mu.Unlock()

	out := protocol.ParticipantList{Enabled: h.roles.isEnabled()}
	for _, e := range entries {
		name := e.name
		if name == "" {
			name = "Unidentified device"
		}
		out.Participants = append(out.Participants, protocol.Participant{
			Name: name,
			Role: h.roles.role(e.conn),
		})
	}
	return out
}

func (h *Hub) broadcastParticipants() {
	h.broadcast(protocol.TypeParticipants, h.participants())
}
