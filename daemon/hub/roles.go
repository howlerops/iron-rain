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
//
// Nil-receiver safe. dropClient is now reachable from the broadcast path (a subscriber whose queue
// overflows tears its connection down), and that path runs for any Hub — including one assembled as
// a struct literal rather than through New, which leaves this registry nil. A cleanup routine that
// panics on a half-built Hub is worse than the leak it was cleaning up.
func (r *roleRegistry) forget(conn *transport.Conn) {
	if r == nil {
		return
	}
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
	return h.requireCapabilityBecause(conn, envID, c, what, "")
}

// requireCapabilityBecause is requireCapability with one extra sentence appended to the refusal.
//
// It exists for the handful of actions that are gated HARDER than the neighbouring ones a user just
// used successfully. A steerer who can prompt the agent, restart it, and write files, and then gets
// "only the owner can do that" on one button, has no way to tell a deliberate boundary from a bug —
// and the honest guess, from their side, is that the button is broken. The refusal has to carry the
// reason, because there is nowhere else for the reason to live: the client renders the control from
// a static layout, not from a capability the daemon told it about.
//
// Pass a `because` only where the answer isn't self-evident from the action's own name. Empty means
// the name is enough ("Only the session owner can revoke a device.").
func (h *Hub) requireCapabilityBecause(conn *transport.Conn, envID string, c capability, what, because string) bool {
	role := h.roles.role(conn)
	if roleAllows(role, c) {
		return true
	}
	who := h.clientName(conn)
	if who == "" {
		who = "an unidentified client"
	}
	log.Printf("roles: DENIED %s to %s (role %s)", what, who, role)
	h.sendErrCode(conn, envID, refusalMessage(c, what, because), protocol.ErrorForbidden)
	return false
}

// refusalMessage renders what the user is told when they are refused.
//
// Split out from the send so it can be asserted directly — these messages are the only place a
// boundary is ever explained. The client renders its controls from a static layout, not from
// capabilities the daemon told it about, so a control that fails without a reason reads as a broken
// feature, and the honest guess from the other side is that the app is buggy rather than that the
// limit is deliberate.
func refusalMessage(c capability, what, because string) string {
	var msg string
	switch c {
	case capApprove, capOwner:
		// Name the ACTION, not just the rule. Every call site already passes a verb phrase for the
		// log line ("run tests", "revoke a device"), and a refusal that reads back the thing the user
		// just tried is the difference between a boundary they can accept and an error they'll retry.
		// The two capabilities share a message on purpose: both mean "owner only" to the person
		// reading it, and the previous split produced "Only the session owner can answer approvals."
		// in response to listing invites.
		msg = "Only the session owner can " + what + "."
	default:
		msg = "You're watching this session. Ask the owner for permission to steer."
	}
	if because != "" {
		msg += " " + because
	}
	return msg
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
	// A grant is targeted by DISPLAY NAME, and a display name is whatever the client said it was —
	// client.identify is ungated and unverified. So two connections can present the same one, and
	// this used to break on the first match while ranging a Go map, whose iteration order is
	// deliberately randomized: the grant landed on one of them at random, the owner had no way to
	// see which, and nothing on the Sharing sheet distinguished the rows.
	//
	// Refusing an ambiguous grant is the whole fix here. Picking wrong hands steering — and on a
	// steerer grant, the ability to prompt an agent holding the owner's credentials — to a
	// connection the owner did not mean to pick, silently. Refusing costs them a rename.
	h.mu.Lock()
	var match *transport.Conn
	matches := 0
	for conn, c := range h.clients {
		if strings.EqualFold(c.displayName(), target) {
			match = conn
			matches++
		}
	}
	h.mu.Unlock()
	if match == nil {
		return false
	}
	if matches > 1 {
		log.Printf("roles: REFUSING to grant %s to %q — %d connected devices are using that exact "+
			"name and there is no way to tell which one you meant", role, target, matches)
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
			// The connection's own key, short. A name is self-declared and two devices can claim
			// the same one; this is the only thing on the row that they cannot both have.
			KeyPrefix: shortPub(hexKey(e.conn.PeerPublicKey())),
		})
	}
	return out
}

func (h *Hub) broadcastParticipants() {
	h.broadcast(protocol.TypeParticipants, h.participants())
}

// requireEditorRead gates the language-server read family.
//
// These are all ways of reading the project's files: the server only answers for a document that
// was opened through lsp.open, which is confined to an allowed root, so the confinement was never
// the gap. The gap was that fs.read and fs.tree next door are capSteer and these were not gated at
// all — so an observer, admitted by a watch-only link and meant to read one session, could open a
// source file and hover, complete, list its symbols and jump to its definitions, which is the same
// access the file browser refuses them.
//
// capSteer, to match the file browser exactly. lsp.rename is already capSteer and lsp.install is
// capOwner, so this puts the read half where the write half already was.
//
// Carries a reason because the boundary is not self-evident: someone who was just reading a
// transcript quite reasonably expects the editor to work, and "you can't do that" on a hover with
// no explanation reads as a broken feature rather than a deliberate limit.
// editorReadCap is the capability the language-server read family requires. Named so the boundary is
// a value a test can read, rather than a literal buried in the helper below — a test that re-states
// the intended capability alongside the code proves only that two copies agree.
const editorReadCap = capSteer

func (h *Hub) requireEditorRead(conn *transport.Conn, envID string) bool {
	return h.requireCapabilityBecause(conn, envID, editorReadCap, "use the code editor",
		"Reading the project's files is the same access as the file browser, which is limited to people who can steer.")
}
