package hub

import (
	"crypto/subtle"
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
	// MaxDevices caps how many devices one link admits. One by default: the link is pasted into a
	// chat, and a chat is a place things get forwarded. A link that admits an unlimited number of
	// devices for 24 hours is a much larger credential than "let Sam watch this session", which is
	// what the owner thinks they are handing over.
	MaxDevices int
}

// full reports whether this invite has admitted as many devices as it is allowed to.
func (inv *invite) full() bool {
	return inv.MaxDevices > 0 && len(inv.Redeemed) >= inv.MaxDevices
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

// create mints a single-device invite with its own secret.
func (r *inviteRegistry) create(label, role string, ttl time.Duration) *invite {
	return r.createFor(label, role, ttl, 1)
}

// createFor mints an invite that admits up to maxDevices devices. maxDevices <= 0 means one (see
// invite.MaxDevices).
func (r *inviteRegistry) createFor(label, role string, ttl time.Duration, maxDevices int) *invite {
	if ttl <= 0 {
		ttl = defaultInviteTTL
	}
	if maxDevices <= 0 {
		maxDevices = 1
	}
	switch role {
	case RoleSteerer, RoleObserver:
	default:
		role = RoleObserver // an invite can never mint an owner
	}
	inv := &invite{
		ID:         randToken(),
		Label:      strings.TrimSpace(label),
		Secret:     "inv_" + randomSecret(16), // 128 bits, independent of every other credential
		Role:       role,
		ExpiresAt:  time.Now().Add(ttl),
		Redeemed:   map[string]bool{},
		MaxDevices: maxDevices,
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
		if subtle.ConstantTimeCompare([]byte(inv.Secret), []byte(secret)) != 1 {
			continue
		}
		if time.Now().After(inv.ExpiresAt) {
			continue // expired invites are simply not credentials any more
		}
		key := hex.EncodeToString(clientPub)
		// A device that already redeemed this invite is reconnecting, not consuming another slot —
		// otherwise a guest whose Wi-Fi dropped would burn the whole invite on a single reconnect.
		if !inv.Redeemed[key] && inv.full() {
			continue
		}
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

// revoke removes an invite and un-links every client that redeemed it, returning the public keys of
// the devices that came in through it so the caller can close their live connections.
//
// Returning them rather than just deleting map entries is the whole point: revoking used to leave
// the guest connected with their granted role until they chose to leave, which means the owner's
// "stop sharing" did nothing to the person actually in the session.
func (r *inviteRegistry) revoke(id string) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	delete(r.byID, id)
	pubs := make([]string, 0, len(inv.Redeemed))
	for pub := range inv.Redeemed {
		delete(r.byPub, pub)
		pubs = append(pubs, pub)
	}
	return pubs, true
}

// sweepExpired prunes lapsed invites and returns the public keys that were relying on them, so their
// live connections can be closed. An invite's expiry has to end the SESSION, not just the link:
// roleForConn is consulted once at connect, so a guest whose invite lapsed mid-session would
// otherwise keep the role it granted indefinitely.
func (r *inviteRegistry) sweepExpired() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var pubs []string
	now := time.Now()
	for id, inv := range r.byID {
		if !now.After(inv.ExpiresAt) {
			continue
		}
		delete(r.byID, id)
		for pub := range inv.Redeemed {
			delete(r.byPub, pub)
			pubs = append(pubs, pub)
		}
		log.Printf("invites: %q expired", inviteLabel(inv))
	}
	return pubs
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

// AcceptSecret is the daemon's authorizer. It reports whether the presented credential is valid at
// all; the ROLE that follows from it is resolved separately per connection (see roleForConn),
// because the handshake happens before a Conn exists to tag.
//
// Four kinds of credential arrive in the same slot, and the order they're checked in matters:
//
//  1. the device's OWN credential — the steady state, and by far the most common, so it goes first;
//  2. a pairing code — single-use and minutes-long, spent here to mint (1);
//  3. an invite secret — a guest, who gets a role rather than a credential;
//  4. the legacy permanent secret — migration only, and on a deadline (credentials.go).
//
// ownerSecret is the pre-upgrade ~/.oculus/secret (or an explicit --secret). configured says the
// operator set it deliberately, which is the difference between "migrate this away" and "leave the
// scripted setup alone".
func (h *Hub) AcceptSecret(ownerSecret string) func(clientPub []byte, presented string) bool {
	return h.acceptSecret(ownerSecret, false)
}

// AcceptConfiguredSecret is AcceptSecret for a secret the operator passed explicitly with --secret.
// That one is a deliberate, scripted credential: it is never auto-retired, because retiring it would
// break the setup on the next restart and the operator, not the daemon, owns that decision.
func (h *Hub) AcceptConfiguredSecret(ownerSecret string) func(clientPub []byte, presented string) bool {
	return h.acceptSecret(ownerSecret, true)
}

func (h *Hub) acceptSecret(ownerSecret string, configured bool) func(clientPub []byte, presented string) bool {
	h.creds().adoptLegacySecret(ownerSecret, configured)
	return func(clientPub []byte, presented string) bool {
		presented = strings.TrimSpace(presented)
		if presented == "" {
			return false
		}
		c := h.creds()

		// 1. The device's own credential. Bound to this public key, so a credential lifted from one
		//    device authenticates nothing from another.
		if h.authenticateDevice(clientPub, presented) {
			return true
		}

		// 2. A single-use pairing code, or the same-Mac bootstrap code. Both enroll the device and
		//    mint it a credential; the pairing code is destroyed in the process, which is what makes a
		//    screenshot of the QR worthless the moment it is used.
		if c.redeemPairCode(presented) {
			_, ok := h.enrollWithCredential(clientPub)
			if ok {
				log.Printf("pairing: enrolled %s… from a pairing code", shortPub(hexKey(clientPub)))
			}
			return ok
		}
		if c.isLocalCode(presented) {
			_, ok := h.enrollWithCredential(clientPub)
			return ok
		}

		// 3. A live invite. Guests are enrolled so they are visible and revocable, but get no
		//    credential of their own — their access has to end when the invite does.
		if inv, ok := h.invites.redeem(clientPub, presented); ok {
			if !h.enrollGuest(clientPub) {
				return false // this device was revoked; an invite must not undo that
			}
			log.Printf("invites: %q redeemed as %s", inviteLabel(inv), inv.Role)
			// Sharing is only meaningful with enforcement on; redeeming an invite turns it on so a
			// guest can't arrive with owner powers because nobody flipped a switch first.
			h.SetRolesEnabled(true)
			return true
		}

		// 4. The legacy permanent secret. Accepting it is a MIGRATION, not a login: the device is
		//    immediately given a credential of its own, and confirming receipt starts the clock that
		//    kills the permanent secret.
		if c.acceptLegacy(presented) {
			_, ok := h.enrollWithCredential(clientPub)
			if ok {
				log.Printf("pairing: %s… presented the old permanent secret; issuing it a per-device credential",
					shortPub(hexKey(clientPub)))
			}
			return ok
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
	// A device the registry knows arrived through an invite, but with no live invite backing it, must
	// NOT fall through to owner. Guests hold no credential, so this should be unreachable — but the
	// consequence of being wrong is a guest silently promoted to owner (which is arbitrary shell on
	// the user's Mac), and that is not a thing to leave resting on one invariant.
	if h.isGuestDevice(pub) {
		return RoleObserver
	}
	return RoleOwner // paired with the owner's own credential
}

// inviteList renders outstanding invites for the sharing UI.
func (h *Hub) inviteList() protocol.InviteList {
	out := protocol.InviteList{}
	for _, inv := range h.invites.list() {
		out.Invites = append(out.Invites, protocol.Invite{
			ID:         inv.ID,
			Label:      inv.Label,
			Role:       inv.Role,
			ExpiresAt:  inv.ExpiresAt.Unix(),
			Redeemed:   len(inv.Redeemed),
			MaxDevices: inv.MaxDevices,
		})
	}
	return out
}

// SetPairURLBuilder installs the function that turns a credential (an invite secret, or a single-use
// pairing code) into a redeemable link.
func (h *Hub) SetPairURLBuilder(f func(secret string) string) {
	h.mu.Lock()
	h.pairURL = f
	h.mu.Unlock()
}
