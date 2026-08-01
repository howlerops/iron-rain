package hub

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// Per-device credentials.
//
// Pairing used to be one permanent shared secret: every device presented the same string, so the
// daemon could not answer "which devices can reach my machine?" and had no way to revoke one. A lost
// phone meant rotating the secret and re-pairing everything you own.
//
// The Noise handshake already proves each client's static public key before any secret is checked, so
// recording that key costs nothing and makes both questions answerable. The secret still gates the
// FIRST connection from a device; from then on the device is a named, revocable entry.
//
// This deliberately does NOT replace the secret with per-device tokens. That would be a bigger change
// to pairing, and the pairing secret's real weakness — it sits in plaintext in UserDefaults on the
// client — is a separate, unfixed problem this must not be mistaken for solving.

// Device is one enrolled client, identified by its static public key.
type Device struct {
	PubHex    string `json:"pub"`
	Label     string `json:"label,omitempty"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	Revoked   bool   `json:"revoked,omitempty"`
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
// The revocation check lives HERE, after the secret matched, precisely so a revoked device that still
// knows the secret is refused — revocation that a valid secret could bypass would mean nothing.
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
func (h *Hub) RevokeDevice(pubHex string) error {
	reg := h.deviceRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	d, ok := reg.byID[pubHex]
	if !ok {
		return fmt.Errorf("no such device")
	}
	d.Revoked = true
	return reg.saveLocked()
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
			This: self != "" && self == d.PubHex,
		})
	}
	return out
}
