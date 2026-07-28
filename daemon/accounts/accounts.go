// Package accounts manages multiple named credentials per agent provider — so you can keep, say, a
// personal and a work Claude/Codex login (or two API keys) and hot-swap which one new sessions use
// without re-logging-in. An account is just a name plus a set of environment overrides (API keys,
// config-dir pointers) that get merged into a session's process env at spawn. The registry persists
// to ~/.oculus/accounts.json and tracks one active account per provider.
package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Account is one named credential set for a provider.
type Account struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Env      map[string]string `json:"env,omitempty"` // merged into a session's process env
}

// Registry is the persisted set of accounts + the active selection per provider.
type Registry struct {
	mu       sync.Mutex
	path     string
	Accounts []Account         `json:"accounts"`
	Active   map[string]string `json:"active"` // provider → account id
}

// Load reads the registry (empty but usable if the file is missing/corrupt).
func Load(path string) *Registry {
	r := &Registry{path: path, Active: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, r)
		if r.Active == nil {
			r.Active = map[string]string{}
		}
	}
	return r
}

func (r *Registry) save() {
	if r.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.path), 0o700)
	if data, err := json.MarshalIndent(r, "", "  "); err == nil {
		// 0600: account env can hold API keys.
		_ = os.WriteFile(r.path, data, 0o600)
	}
}

// List returns a copy of all accounts.
func (r *Registry) List() []Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Account(nil), r.Accounts...)
}

// ActiveID returns the active account id for a provider ("" if none).
func (r *Registry) ActiveID(provider string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Active[provider]
}

// Upsert adds or updates an account (by ID; a new ID is minted when empty). The first account for a
// provider becomes its active one. Returns the stored account.
func (r *Registry) Upsert(a Account) Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.ID == "" {
		a.ID = "acct_" + randHex()
	}
	found := false
	for i := range r.Accounts {
		if r.Accounts[i].ID == a.ID {
			r.Accounts[i] = a
			found = true
			break
		}
	}
	if !found {
		r.Accounts = append(r.Accounts, a)
	}
	if r.Active[a.Provider] == "" {
		r.Active[a.Provider] = a.ID // first account for this provider is active by default
	}
	r.save()
	return a
}

// Delete removes an account; if it was active, the active selection falls back to another account
// for that provider (or clears).
func (r *Registry) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var provider string
	out := r.Accounts[:0]
	for _, a := range r.Accounts {
		if a.ID == id {
			provider = a.Provider
			continue
		}
		out = append(out, a)
	}
	r.Accounts = out
	if provider != "" && r.Active[provider] == id {
		delete(r.Active, provider)
		for _, a := range r.Accounts { // pick another account for this provider, if any
			if a.Provider == provider {
				r.Active[provider] = a.ID
				break
			}
		}
	}
	r.save()
}

// SetActive selects the active account for a provider (no-op if the id isn't one of that provider's).
func (r *Registry) SetActive(provider, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.Accounts {
		if a.ID == id && a.Provider == provider {
			r.Active[provider] = id
			r.save()
			return true
		}
	}
	return false
}

// EnvFor returns the active account's env overrides for a provider (nil if none) — merged into a new
// session's process env at spawn.
func (r *Registry) EnvFor(provider string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.Active[provider]
	if id == "" {
		return nil
	}
	for _, a := range r.Accounts {
		if a.ID == id {
			return cloneEnv(a.Env)
		}
	}
	return nil
}

func cloneEnv(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func randHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
