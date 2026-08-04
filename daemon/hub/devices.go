package hub

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// The device registry: who may reach this machine, and with what.
//
// Every device is identified by the static public key it presents in the handshake. Two things were
// wrong with that identity before, and both had to be fixed together or neither meant anything:
//
//  1. The key was NOT stable. The app generated a fresh keypair on every launch, so a revoked phone
//     came back as a brand-new device on its next cold start, took the first-sight branch below, and
//     was authorized. The registry was a list of app launches, not of devices. The client now
//     persists its key in the Keychain (ThisDeviceOnly), which is what gives revocation something to
//     bind to.
//  2. The key was not a CREDENTIAL. Every device presented the same permanent shared secret, so
//     revocation could always be undone by the revoked device simply presenting it again from a new
//     key. Each enrolled device now holds its own 256-bit credential, minted here at enrollment and
//     stored only as a hash. Revoking clears it.
//
// The comment this replaces claimed a Noise handshake proves the client's key. There is no Noise
// handshake — the channel is static-static X25519 ECDH (daemon/crypto), and the client's key is
// asserted in a plaintext hello, not proved. What the channel DOES give us is that a device
// credential is useless without the matching private key, since a peer that can't derive the channel
// can't present anything at all. That is the property the per-device credential leans on, and it is
// worth stating accurately rather than borrowing a guarantee the code doesn't implement.

// Device is one enrolled client, identified by its static public key.
type Device struct {
	PubHex    string `json:"pub"`
	Label     string `json:"label,omitempty"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	Revoked   bool   `json:"revoked,omitempty"`
	// CredHash is the SHA-256 of this device's own credential. The credential itself is never stored:
	// devices.json should be a list of who may connect, not a set of keys to the machine.
	CredHash string `json:"cred,omitempty"`
	// CredIssued is when that credential was minted, so a UI can show "paired 3 months ago" and the
	// owner can spot an enrollment they don't recognise.
	CredIssued int64 `json:"cred_issued,omitempty"`
	// Guest marks a device that came in through an invite. Guests deliberately hold NO credential:
	// their access lives and dies with the invite, so an expired or revoked invite ends it completely
	// instead of leaving behind a device that can re-authenticate on its own.
	Guest bool `json:"guest,omitempty"`
}

type deviceRegistry struct {
	mu   sync.Mutex
	path string
	byID map[string]*Device
}

func hexKey(pub []byte) string { return hex.EncodeToString(pub) }

// SetDevicesPath points the registry at its file and loads what's there. Without a path the registry
// stays in memory: enrollment still works for the current process, revocation just doesn't outlive it.
func (h *Hub) SetDevicesPath(path string) {
	h.mu.Lock()
	if h.devices == nil {
		h.devices = &deviceRegistry{byID: map[string]*Device{}}
	}
	reg := h.devices
	h.mu.Unlock()

	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.path = path
	reg.byID = map[string]*Device{}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var list []*Device
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for _, d := range list {
		reg.byID[d.PubHex] = d
	}
}

func (h *Hub) deviceRegistry() *deviceRegistry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.devices == nil {
		h.devices = &deviceRegistry{byID: map[string]*Device{}}
	}
	return h.devices
}

// enroll records a successful pairing, or reports false when the device has been revoked.
//
// The revocation check lives HERE, after the credential matched, precisely so a revoked device that
// still knows a valid credential is refused — revocation a valid credential could bypass would mean
// nothing.
func (h *Hub) enroll(pub []byte) bool {
	if len(pub) == 0 {
		return true // in-process/local callers with no key material: nothing to enroll or revoke
	}
	reg := h.deviceRegistry()
	id := hexKey(pub)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	now := time.Now().Unix()
	d, ok := reg.byID[id]
	if !ok {
		reg.byID[id] = &Device{PubHex: id, FirstSeen: now, LastSeen: now}
		reg.saveLocked()
		return true
	}
	if d.Revoked {
		return false
	}
	d.LastSeen = now
	reg.saveLocked()
	return true
}

// enrollWithCredential enrolls a device and mints the credential it will present from now on. This
// is what SPENDS a pairing code: after this the device no longer needs — and no longer has — the
// short-lived thing it arrived with.
//
// Re-enrolling an existing device replaces its credential rather than keeping the old one alive. A
// device that re-pairs is usually a device whose stored credential was lost (reinstall, restored
// backup), and leaving the previous one valid would mean every re-pair permanently widened the set
// of strings that open the machine.
func (h *Hub) enrollWithCredential(pub []byte) (string, bool) {
	if len(pub) == 0 {
		return "", true // in-process/local callers: no key to bind a credential to
	}
	reg := h.deviceRegistry()
	id := hexKey(pub)
	cred := "dc_" + randomSecret(32)
	if cred == "dc_" {
		return "", false // crypto/rand failed; refuse rather than mint a predictable credential
	}
	reg.mu.Lock()
	now := time.Now().Unix()
	d, ok := reg.byID[id]
	if !ok {
		d = &Device{PubHex: id, FirstSeen: now}
		reg.byID[id] = d
	} else if d.Revoked {
		reg.mu.Unlock()
		return "", false
	}
	d.LastSeen = now
	d.Guest = false
	d.CredHash = credHash(cred)
	d.CredIssued = now
	reg.saveLocked()
	reg.mu.Unlock()

	// Park it for delivery: the handshake carries no room for a credential (and must not grow a
	// field), so the daemon hands it over as the first frame once the connection is up.
	h.creds().stashPending(id, cred)
	return cred, true
}

// enrollGuest records an invite-authenticated device so it shows up in the device list and can be
// revoked like any other. It mints no credential — see Device.Guest.
func (h *Hub) enrollGuest(pub []byte) bool {
	if len(pub) == 0 {
		return true
	}
	reg := h.deviceRegistry()
	id := hexKey(pub)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	now := time.Now().Unix()
	d, ok := reg.byID[id]
	if !ok {
		reg.byID[id] = &Device{PubHex: id, FirstSeen: now, LastSeen: now, Guest: true}
		reg.saveLocked()
		return true
	}
	if d.Revoked {
		return false
	}
	d.LastSeen = now
	reg.saveLocked()
	return true
}

// authenticateDevice checks a presented credential against the enrolled device that owns it.
//
// The credential is matched against THIS device's hash only, never against every device's — a
// credential minted for one phone must not authenticate another, or per-device revocation would be
// decorative.
func (h *Hub) authenticateDevice(pub []byte, presented string) bool {
	if len(pub) == 0 || presented == "" {
		return false
	}
	reg := h.deviceRegistry()
	id := hexKey(pub)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	d, ok := reg.byID[id]
	if !ok || d.Revoked || d.CredHash == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(d.CredHash), []byte(credHash(presented))) != 1 {
		return false
	}
	d.LastSeen = time.Now().Unix()
	reg.saveLocked()
	return true
}

// isGuestDevice reports whether an enrolled device arrived through an invite.
func (h *Hub) isGuestDevice(pub []byte) bool {
	if len(pub) == 0 {
		return false
	}
	reg := h.deviceRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	d, ok := reg.byID[hexKey(pub)]
	return ok && d.Guest
}

// Devices lists every enrolled, non-revoked device.
func (h *Hub) Devices() []Device {
	reg := h.deviceRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	out := make([]Device, 0, len(reg.byID))
	for _, d := range reg.byID {
		if !d.Revoked {
			out = append(out, *d)
		}
	}
	return out
}

// RevokeDevice locks out one device. The entry is KEPT and marked revoked rather than deleted: a
// deleted entry would simply re-enroll on the next connection, which is the opposite of revoking.
//
// Two things happen here that did not before, and revocation was close to a no-op without either:
//
//   - The device's credential is destroyed. Marking a flag while leaving the credential valid meant
//     that un-revoking (or any code path that took the first-sight branch) handed access straight
//     back.
//   - Every LIVE connection from that device is closed. Revocation used to set a bool and rebroadcast
//     a list; the stolen phone stayed connected, kept its role, and kept driving the agent until it
//     chose to disconnect. "Revoke" that only applies to the NEXT connection is not what anyone
//     pressing that button is asking for — they are asking for the device that is on their machine
//     right now to be off it.
func (h *Hub) RevokeDevice(pubHex string) error {
	reg := h.deviceRegistry()
	reg.mu.Lock()
	d, ok := reg.byID[pubHex]
	if !ok {
		reg.mu.Unlock()
		return fmt.Errorf("no such device")
	}
	d.Revoked = true
	d.CredHash = ""
	err := reg.saveLocked()
	reg.mu.Unlock()

	if n := h.closeDeviceConns(pubHex, "device revoked"); n > 0 {
		log.Printf("devices: revoked %s… and closed %d live connection(s)", shortPub(pubHex), n)
	} else {
		log.Printf("devices: revoked %s…", shortPub(pubHex))
	}
	return err
}

// closeDeviceConns drops every live connection whose handshake public key matches pubHex, and
// reports how many it closed.
//
// dropClient unregisters the client and stops its writer; Close tears down the socket so the read
// loop returns instead of sitting in Recv. Both are needed: unregistering alone leaves the peer
// holding an open, authenticated socket, and closing alone leaves a registered client whose writes
// go nowhere.
func (h *Hub) closeDeviceConns(pubHex, why string) int {
	if pubHex == "" {
		return 0
	}
	h.mu.Lock()
	doomed := make([]*transport.Conn, 0, 1)
	for conn := range h.clients {
		if hexKey(conn.PeerPublicKey()) == pubHex {
			doomed = append(doomed, conn)
		}
	}
	h.mu.Unlock()
	for _, conn := range doomed {
		log.Printf("devices: closing live connection from %s… (%s)", shortPub(pubHex), why)
		h.dropClient(conn)
		_ = conn.Close()
	}
	return len(doomed)
}

// shortPub renders a public key for a log line. The full 64 hex characters say nothing a reader can
// use and push everything else off the line.
func shortPub(pubHex string) string {
	if len(pubHex) > 12 {
		return pubHex[:12]
	}
	return pubHex
}

// LabelDevice gives a device a human name, so the list reads "Jacob's iPhone" rather than 64 hex
// characters.
func (h *Hub) LabelDevice(pubHex, label string) error {
	reg := h.deviceRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	d, ok := reg.byID[pubHex]
	if !ok {
		return fmt.Errorf("no such device")
	}
	d.Label = label
	return reg.saveLocked()
}

// saveLocked writes the registry. Caller holds mu.
func (r *deviceRegistry) saveLocked() error {
	if r.path == "" {
		return nil
	}
	list := make([]*Device, 0, len(r.byID))
	for _, d := range r.byID {
		list = append(list, d)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	// 0600: this records which keys may drive the user's machine.
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// deviceList renders the registry for a client, marking which entry is the asking device so a UI can
// avoid offering "revoke" on the connection it is speaking over.
func (h *Hub) deviceList(conn *transport.Conn) protocol.DeviceList {
	var self string
	if conn != nil {
		self = hexKey(conn.PeerPublicKey())
	}
	devs := h.Devices()
	out := protocol.DeviceList{Devices: make([]protocol.DeviceInfo, 0, len(devs))}
	for _, d := range devs {
		out.Devices = append(out.Devices, protocol.DeviceInfo{
			Pub: d.PubHex, Label: d.Label, FirstSeen: d.FirstSeen, LastSeen: d.LastSeen,
			This:  self != "" && self == d.PubHex,
			Guest: d.Guest,
		})
	}
	return out
}
