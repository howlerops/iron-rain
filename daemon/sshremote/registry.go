package sshremote

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Registry is the persisted set of remote hosts (~/.oculus/remotes.json).
type Registry struct {
	mu    sync.Mutex
	path  string
	Hosts []Host `json:"hosts"`
}

// LoadRegistry reads the host registry (empty but usable if missing/corrupt).
func LoadRegistry(path string) *Registry {
	r := &Registry{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, r)
	}
	return r
}

func (r *Registry) save() {
	if r.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.path), 0o700)
	if data, err := json.MarshalIndent(r, "", "  "); err == nil {
		_ = os.WriteFile(r.path, data, 0o600)
	}
}

// List returns a copy of the registered hosts.
func (r *Registry) List() []Host {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Host(nil), r.Hosts...)
}

// Get returns a host by id (ok=false if not found).
func (r *Registry) Get(id string) (Host, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.Hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}

// Upsert adds or updates a host (minting an ID when empty). Returns the stored host.
func (r *Registry) Upsert(h Host) Host {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h.ID == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		h.ID = "host_" + hex.EncodeToString(b[:])
	}
	for i := range r.Hosts {
		if r.Hosts[i].ID == h.ID {
			r.Hosts[i] = h
			r.save()
			return h
		}
	}
	r.Hosts = append(r.Hosts, h)
	r.save()
	return h
}

// Delete removes a host by id.
func (r *Registry) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.Hosts[:0]
	for _, h := range r.Hosts {
		if h.ID != id {
			out = append(out, h)
		}
	}
	r.Hosts = out
	r.save()
}
