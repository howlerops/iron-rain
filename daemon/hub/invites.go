package hub

import (
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Invites: how someone who isn't you gets connected.
//
// Roles alone weren't enough to actually share a session — every device still had to pair with the
// daemon's ONE secret, which is owner-equivalent. Handing that to a guest so they can watch would
// defeat the entire point of having roles.
//
// So an invite carries its OWN secret and its own role. A guest who redeems it is authenticated
// (the transport is unchanged; it just accepts more than one credential) but arrives as an observer
// unless the invite says otherwise. Revoking the invite drops the credential; it never touches the
// owner's.
//
// Invites expire by default. A share link that works forever is a credential someone will paste into
// a chat log and forget about.

// defaultInviteTTL is how long a new invite stays redeemable.
const defaultInviteTTL = 24 * time.Hour

// invite is one outstanding share credential.
type invite struct {
	ID        string
	Label     string
	Secret    string
	Role      string
	ExpiresAt time.Time
	// Redeemed records the client public keys that used this invite, so their connections can be
	// given the invite's role.
	Redeemed map[string]bool
}

// inviteRegistry holds outstanding invites. In memory only: an invite is a short-lived credential,
// and persisting one across restarts would quietly extend its life past the expiry the user chose.
type inviteRegistry struct {
	mu    sync.RWMutex
	byID  map[string]*invite
	byPub map[string]string // client pubkey (hex) -> invite id
}

func newInviteRegistry() *inviteRegistry {
	return &inviteRegistry{byID: map[string]*invite{}, byPub: map[string]string{}}
}

// create mints an invite with its own secret.
func (r *inviteRegistry) create(label, role string, ttl time.Duration) *invite {
	if ttl <= 0 {
		ttl = defaultInviteTTL
	}
	switch role {
	case RoleSteerer, RoleObserver:
	default:
		role = RoleObserver // an invite can never mint an owner
	}
	inv := &invite{
		ID:        randToken(),
		Label:     strings.TrimSpace(label),
		Secret:    randToken() + randToken(), // 128 bits, independent of the daemon secret
		Role:      role,
		ExpiresAt: time.Now().Add(ttl),
		Redeemed:  map[string]bool{},
	}
	r.mu.Lock()
	r.byID[inv.ID] = inv
	r.mu.Unlock()
	return inv
}

// redeem checks a presented secret against live invites. On success it remembers which client
// redeemed it so the resulting connection can be assigned the invite's role.
func (r *inviteRegistry) redeem(clientPub []byte, secret string) (*invite, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, inv := range r.byID {
		if inv.Secret != secret {
			continue
		}
		if time.Now().After(inv.ExpiresAt) {
			continue // expired invites are simply not credentials any more
		}
		key := hex.EncodeToString(clientPub)
		inv.Redeemed[key] = true
		r.byPub[key] = inv.ID
		return inv, true
	}
	return nil, false
}

// roleFor returns the role a client should get, based on the invite it redeemed. ok=false means this
// client didn't come in through an invite (i.e. it used the owner's own credential).
func (r *inviteRegistry) roleFor(clientPub []byte) (string, bool) {
	if len(clientPub) == 0 {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byPub[hex.EncodeToString(clientPub)]
	if !ok {
		return "", false
	}
	inv, ok := r.byID[id]
	if !ok || time.Now().After(inv.ExpiresAt) {
		return "", false
	}
	return inv.Role, true
}

// revoke removes an invite and un-links every client that redeemed it.
func (r *inviteRegistry) revoke(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.byID[id]
	if !ok {
		return false
	}
	delete(r.byID, id)
	for pub := range inv.Redeemed {
		delete(r.byPub, pub)
	}
	return true
}

// list returns the live (unexpired) invites, pruning any that have lapsed.
func (r *inviteRegistry) list() []*invite {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*invite, 0, len(r.byID))
	for id, inv := range r.byID {
		if time.Now().After(inv.ExpiresAt) {
			delete(r.byID, id)
			for pub := range inv.Redeemed {
				delete(r.byPub, pub)
			}
			continue
		}
		out = append(out, inv)
	}
	return out
}

// AcceptSecret is the daemon's authorizer: the owner's secret, or a live invite. It reports whether
// the credential is valid at all; the ROLE that follows from it is resolved separately per
// connection (see roleForConn), because the handshake happens before a Conn exists to tag.
func (h *Hub) AcceptSecret(ownerSecret string) func(clientPub []byte, presented string) bool {
	return func(clientPub []byte, presented string) bool {
		if presented == ownerSecret {
			return true
		}
		if inv, ok := h.invites.redeem(clientPub, presented); ok {
			log.Printf("invites: %q redeemed as %s", inviteLabel(inv), inv.Role)
			// Sharing is only meaningful with enforcement on; redeeming an invite turns it on so a
			// guest can't arrive with owner powers because nobody flipped a switch first.
			h.SetRolesEnabled(true)
			return true
		}
		return false
	}
}

func inviteLabel(inv *invite) string {
	if inv.Label != "" {
		return inv.Label
	}
	return "invite " + inv.ID
}

// roleForConn resolves the role a freshly-connected client should hold.
func (h *Hub) roleForConn(pub []byte) string {
	if role, ok := h.invites.roleFor(pub); ok {
		return role
	}
	return RoleOwner // paired with the owner's own credential
}

// inviteList renders outstanding invites for the sharing UI.
func (h *Hub) inviteList() protocol.InviteList {
	out := protocol.InviteList{}
	for _, inv := range h.invites.list() {
		out.Invites = append(out.Invites, protocol.Invite{
			ID:        inv.ID,
			Label:     inv.Label,
			Role:      inv.Role,
			ExpiresAt: inv.ExpiresAt.Unix(),
			Redeemed:  len(inv.Redeemed),
		})
	}
	return out
}

// SetPairURLBuilder installs the function that turns an invite secret into a redeemable link.
func (h *Hub) SetPairURLBuilder(f func(secret string) string) {
	h.mu.Lock()
	h.pairURL = f
	h.mu.Unlock()
}
