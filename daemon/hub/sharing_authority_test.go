package hub

import (
	"sort"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/transport"
)

// Turning the sharing switch OFF promoted every connected guest to owner.
//
// With enforcement off, roleRegistry.role() short-circuits to RoleOwner for every connection. That
// is deliberate and right for a solo setup where every device is yours. It is not right for someone
// admitted by an invite: their socket is already open and never re-authenticates, so flipping the
// switch upgraded a watch-only guest in place. On that same connection they could then answer
// approvals, run commands as the owner, revoke the owner's own devices, and mint a pairing code to
// come back later as a permanent device — the durable part, which outlives the switch being flipped
// back.
//
// The owner reads that switch as "stop gating my own machines". It cannot also mean "hand the room
// to whoever happens to be standing in it". Invite revocation already closes the connections a link
// let in, for exactly this reason; this path simply never did the same.
func TestTurningSharingOffCutsOffGuestsAndOnlyGuests(t *testing.T) {
	h := &Hub{devices: &deviceRegistry{byID: map[string]*Device{
		"aa11": {PubHex: "aa11", Guest: false},               // the owner's Mac
		"bb22": {PubHex: "bb22", Guest: true},                // admitted by an invite
		"cc33": {PubHex: "cc33", Guest: true, Revoked: true}, // already gone
		"dd44": {PubHex: "dd44", Guest: false},               // the owner's phone
	}}}

	got := h.guestsToDisconnect()
	sort.Strings(got)
	if len(got) != 1 || got[0] != "bb22" {
		t.Fatalf("guests to disconnect = %v, want exactly the live guest. Anything missing here "+
			"stays connected and is promoted to owner by the same flip; anything extra "+
			"disconnects one of the owner's own devices for no reason", got)
	}
}

// And the handler must actually call it, only when DISABLING.
func TestTheRolesEnableHandlerDisconnectsGuestsWhenTurningOff(t *testing.T) {
	body := dispatchArm(t, "case protocol.TypeRolesEnable:", "case protocol.TypeRoleGrant:")
	if !strings.Contains(body, "guestsToDisconnect") {
		t.Error("roles.enable never disconnects anyone: with enforcement off every open connection " +
			"reads as owner, including an invite guest who never re-authenticated")
	}
	if !strings.Contains(body, "if !req.Enabled") {
		t.Error("the disconnect is not gated on DISABLING — turning sharing ON must not drop anyone")
	}
}

// A grant aimed by display name could land on the wrong connection.
//
// client.identify is ungated and unverified, so a name is whatever a device claims. grantRole ranged
// over a Go map — whose iteration order is deliberately randomized — and broke on the first match.
// With two connections claiming the same name the grant went to one of them at random, the owner had
// no way to see which, and the Sharing sheet rendered two identical rows. On a steerer grant that
// hands the ability to prompt an agent running with the owner's credentials to whoever won the coin
// toss.
func TestAnAmbiguousGrantIsRefusedRatherThanGuessed(t *testing.T) {
	colleague, attacker := &transport.Conn{}, &transport.Conn{}
	h := &Hub{
		clients: map[*transport.Conn]*hubClient{
			colleague: {conn: colleague, name: "Sam's iPad", ch: make(chan []byte, 8), done: make(chan struct{})},
			attacker:  {conn: attacker, name: "Sam's iPad", ch: make(chan []byte, 8), done: make(chan struct{})},
		},
		roles: newRoleRegistry(),
	}
	h.roles.SetEnabled(true)

	if h.grantRole("Sam's iPad", RoleSteerer) {
		t.Fatal("granted steering to one of two connections claiming the same name, chosen by Go " +
			"map iteration order — the owner cannot see which one got it")
	}
	for conn, name := range map[*transport.Conn]string{colleague: "colleague", attacker: "attacker"} {
		if got := h.roles.role(conn); got != RoleObserver {
			t.Errorf("the %s ended up a %q; a refused grant must move nobody", name, got)
		}
	}
}

// An unambiguous grant must still work — refusing everything would be its own bug.
func TestAnUnambiguousGrantStillLands(t *testing.T) {
	sam, alex := &transport.Conn{}, &transport.Conn{}
	h := &Hub{
		clients: map[*transport.Conn]*hubClient{
			sam:  {conn: sam, name: "Sam's iPad", ch: make(chan []byte, 8), done: make(chan struct{})},
			alex: {conn: alex, name: "Alex's Mac", ch: make(chan []byte, 8), done: make(chan struct{})},
		},
		roles: newRoleRegistry(),
	}
	h.roles.SetEnabled(true)

	if !h.grantRole("Sam's iPad", RoleSteerer) {
		t.Fatal("a grant with exactly one matching connection was refused")
	}
	if got := h.roles.role(sam); got != RoleSteerer {
		t.Errorf("Sam is a %q, want steerer", got)
	}
	if got := h.roles.role(alex); got != RoleObserver {
		t.Errorf("the grant also moved Alex, who was not named: %q", got)
	}
}

// dispatchArm returns the source of one arm of the dispatch switch.
func dispatchArm(t *testing.T, from, to string) string {
	t.Helper()
	src, err := readFileString("hub.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(src, from)
	if i < 0 {
		t.Fatalf("could not find %q", from)
	}
	j := strings.Index(src[i:], to)
	if j < 0 {
		t.Fatalf("could not find %q after %q", to, from)
	}
	return src[i : i+j]
}
