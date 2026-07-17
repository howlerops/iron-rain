package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProviderCreds persists per-provider auth (OAuth app creds + the user access token).
type ProviderCreds struct {
	ClientID      string `json:"client_id,omitempty"`
	ClientSecret  string `json:"client_secret,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
	Token         string `json:"token,omitempty"`
}

// Config is ~/.oculus/integrations.json.
type Config struct {
	Linear ProviderCreds `json:"linear"`
	Jira   ProviderCreds `json:"jira"`
}

// Manager owns the connected trackers, a merged issue cache, and the poll loop.
type Manager struct {
	mu        sync.Mutex
	path      string
	cfg       Config
	providers map[string]Provider
	cache     []Issue
	onUpdate  func([]Issue)
	pending   map[string]string // oauth state -> provider
}

// NewManager loads config from path and reconnects any provider that has a saved token.
func NewManager(path string, onUpdate func([]Issue)) *Manager {
	m := &Manager{path: path, providers: map[string]Provider{}, onUpdate: onUpdate}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m.cfg)
	}
	if m.cfg.Linear.Token != "" {
		if p, err := newAdapter("linear", m.cfg.Linear.Token); err == nil {
			m.providers["linear"] = p
		}
	}
	return m
}

func newAdapter(name, token string) (Provider, error) {
	switch name {
	case "linear":
		return NewLinear(token), nil
	case "jira":
		// token = "https://site.atlassian.net|email|apitoken"
		parts := strings.SplitN(token, "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("jira token must be site|email|apitoken")
		}
		return NewJira(parts[0], parts[1], parts[2]), nil
	default:
		return nil, fmt.Errorf("unknown tracker: %q", name)
	}
}

// Connect sets a provider's token, persists it, creates the adapter, and refreshes.
func (m *Manager) Connect(ctx context.Context, name, token string) error {
	p, err := newAdapter(name, token)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.providers[name] = p
	switch name {
	case "linear":
		m.cfg.Linear.Token = token
	case "jira":
		m.cfg.Jira.Token = token
	}
	m.save()
	m.mu.Unlock()
	return m.Refresh(ctx)
}

// AddProvider registers a provider directly (used by tests + non-token backends).
func (m *Manager) AddProvider(name string, p Provider) {
	m.mu.Lock()
	m.providers[name] = p
	m.mu.Unlock()
}

// Connected returns the names of connected providers.
func (m *Manager) Connected() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.providers))
	for n := range m.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Issues returns the current merged cache.
func (m *Manager) Issues() []Issue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Issue(nil), m.cache...)
}

// Provider returns a connected provider by name (nil if not connected).
func (m *Manager) Provider(name string) Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providers[name]
}

// States returns a provider's workflow states (kanban columns).
func (m *Manager) States(ctx context.Context, provider, teamID string) ([]State, error) {
	p := m.Provider(provider)
	if p == nil {
		return nil, fmt.Errorf("%s not connected", provider)
	}
	return p.WorkflowStates(ctx, teamID)
}

// Refresh pulls assigned issues from every provider, merges, caches, and notifies.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	provs := make([]Provider, 0, len(m.providers))
	for _, p := range m.providers {
		provs = append(provs, p)
	}
	m.mu.Unlock()

	var merged []Issue
	var firstErr error
	for _, p := range provs {
		got, err := p.ListAssigned(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		merged = append(merged, got...)
	}
	m.mu.Lock()
	m.cache = merged
	cb := m.onUpdate
	m.mu.Unlock()
	if cb != nil {
		cb(merged)
	}
	return firstErr
}

// StartPolling refreshes on a ticker until ctx is done (call once).
func (m *Manager) StartPolling(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if len(m.Connected()) > 0 {
					_ = m.Refresh(ctx)
				}
			}
		}
	}()
}

func (m *Manager) save() {
	if m.path == "" {
		return
	}
	if dir := filepath.Dir(m.path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if data, err := json.MarshalIndent(m.cfg, "", "  "); err == nil {
		_ = os.WriteFile(m.path, data, 0o600)
	}
}
