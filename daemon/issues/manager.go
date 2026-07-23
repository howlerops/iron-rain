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
	cache      []Issue
	onUpdate   func([]Issue)
	pending    map[string]string // oauth state -> provider
	authErrors map[string]string // provider -> last token-refresh error (drives the "reconnect" pill)
}

// TokenRefresher is a provider whose OAuth token can be proactively refreshed (Jira).
type TokenRefresher interface {
	RefreshToken(ctx context.Context) error
}

// NewManager loads config from path and reconnects any provider that has a saved token.
func NewManager(path string, onUpdate func([]Issue)) *Manager {
	m := &Manager{path: path, providers: map[string]Provider{}, onUpdate: onUpdate, authErrors: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m.cfg)
	}
	// Reconnect every provider that has a saved token. Looping over a name->token
	// map keeps new providers from being silently forgotten here.
	for name, token := range map[string]string{
		"linear": m.cfg.Linear.Token,
		"jira":   m.cfg.Jira.Token,
	} {
		if token == "" {
			continue
		}
		if p, err := m.newAdapter(name, token); err == nil {
			m.providers[name] = p
		}
	}
	return m
}

func (m *Manager) newAdapter(name, token string) (Provider, error) {
	switch name {
	case "linear":
		return NewLinear(token), nil
	case "jira":
		// OAuth: "oauth|cloudid|access|refresh". Basic: "site|email|apitoken".
		if strings.HasPrefix(token, "oauth|") {
			parts := strings.SplitN(token, "|", 4)
			if len(parts) != 4 {
				return nil, fmt.Errorf("jira oauth token must be oauth|cloudid|access|refresh")
			}
			cloudID := parts[1]
			m.mu.Lock()
			clientID, clientSecret := m.cfg.Jira.ClientID, m.cfg.Jira.ClientSecret
			m.mu.Unlock()
			// Persist rotated tokens so the next start (and the next refresh) has the current pair.
			onRefresh := func(access, refresh string) {
				m.mu.Lock()
				m.cfg.Jira.Token = strings.Join([]string{"oauth", cloudID, access, refresh}, "|")
				cfg := m.cfg
				m.mu.Unlock()
				m.save(cfg)
			}
			return NewJiraOAuth(cloudID, parts[2], parts[3], clientID, clientSecret, onRefresh), nil
		}
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
	p, err := m.newAdapter(name, token)
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
	// Snapshot the config under the lock, then persist without holding it so the
	// disk write can't block Issues()/Refresh()/the poll loop on a slow FS.
	cfg := m.cfg
	m.mu.Unlock()
	m.save(cfg)
	return m.Refresh(ctx)
}

// JiraSites lists every Atlassian site the connected Jira OAuth token can access, plus the cloud id
// currently in use — so the app can let the user switch when an org has more than one site (the
// cause of "connected but no tickets": the daemon was routing to the wrong/unused site).
func (m *Manager) JiraSites(ctx context.Context) (sites []JiraSiteInfo, current string, err error) {
	m.mu.Lock()
	tok := m.cfg.Jira.Token
	m.mu.Unlock()
	if !strings.HasPrefix(tok, "oauth|") {
		return nil, "", fmt.Errorf("jira isn't connected via OAuth")
	}
	parts := strings.SplitN(tok, "|", 4)
	if len(parts) != 4 {
		return nil, "", fmt.Errorf("jira token malformed")
	}
	current = parts[1]
	sites, err = jiraAccessibleSites(ctx, parts[2])
	return sites, current, err
}

// SetJiraSite switches the active Jira site (cloud id). The OAuth access/refresh tokens work across
// all of the org's sites — only the cloud id routes the API — so this just rewrites the composite
// token, rebuilds the adapter, and refreshes. No re-auth needed.
func (m *Manager) SetJiraSite(ctx context.Context, cloudID string) error {
	if cloudID == "" {
		return fmt.Errorf("no site chosen")
	}
	m.mu.Lock()
	tok := m.cfg.Jira.Token
	m.mu.Unlock()
	parts := strings.SplitN(tok, "|", 4)
	if !strings.HasPrefix(tok, "oauth|") || len(parts) != 4 {
		return fmt.Errorf("jira isn't connected via OAuth")
	}
	newToken := strings.Join([]string{"oauth", cloudID, parts[2], parts[3]}, "|")
	p, err := m.newAdapter("jira", newToken)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.providers["jira"] = p
	m.cfg.Jira.Token = newToken
	delete(m.authErrors, "jira")
	cfg := m.cfg
	m.mu.Unlock()
	m.save(cfg)
	return m.Refresh(ctx)
}

// AddProvider registers a provider directly (used by tests + non-token backends).
func (m *Manager) AddProvider(name string, p Provider) {
	m.mu.Lock()
	m.providers[name] = p
	m.mu.Unlock()
}

// SetOAuthApp stores a provider's OAuth app credentials (client_id/secret) so the OAuth flow can
// start without hand-editing integrations.json. These are the OAuth APP's creds, not a user token.
func (m *Manager) SetOAuthApp(provider, clientID, clientSecret string) error {
	m.mu.Lock()
	switch provider {
	case "linear":
		m.cfg.Linear.ClientID, m.cfg.Linear.ClientSecret = clientID, clientSecret
	case "jira":
		m.cfg.Jira.ClientID, m.cfg.Jira.ClientSecret = clientID, clientSecret
	default:
		m.mu.Unlock()
		return fmt.Errorf("oauth not supported for %q", provider)
	}
	cfg := m.cfg
	m.mu.Unlock()
	m.save(cfg)
	return nil
}

// OAuthApps reports which providers have an OAuth app configured (client_id present), so the app
// can show the OAuth button vs a "add your OAuth app" prompt.
func (m *Manager) OAuthApps() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if m.cfg.Linear.ClientID != "" {
		out = append(out, "linear")
	}
	if m.cfg.Jira.ClientID != "" {
		out = append(out, "jira")
	}
	return out
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

// hasProviders reports whether any provider is connected. It avoids the slice
// allocation + sort that Connected() does, for the hot poll-tick path.
func (m *Manager) hasProviders() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.providers) > 0
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

// Detail returns a single issue plus its comments from the named provider.
func (m *Manager) Detail(ctx context.Context, provider, issueID string) (Issue, []Comment, error) {
	p := m.Provider(provider)
	if p == nil {
		return Issue{}, nil, fmt.Errorf("%s not connected", provider)
	}
	return p.Detail(ctx, issueID)
}

// Update applies partial edits to an issue and returns the updated issue.
func (m *Manager) Update(ctx context.Context, provider, issueID string, f UpdateFields) (Issue, error) {
	p := m.Provider(provider)
	if p == nil {
		return Issue{}, fmt.Errorf("%s not connected", provider)
	}
	return p.Update(ctx, issueID, f)
}

// AddComment posts a new comment on an issue (wraps the provider's Comment method).
func (m *Manager) AddComment(ctx context.Context, provider, issueID, body string) error {
	p := m.Provider(provider)
	if p == nil {
		return fmt.Errorf("%s not connected", provider)
	}
	return p.Comment(ctx, issueID, body)
}

// EditComment replaces the body of an existing comment.
func (m *Manager) EditComment(ctx context.Context, provider, commentID, body string) error {
	p := m.Provider(provider)
	if p == nil {
		return fmt.Errorf("%s not connected", provider)
	}
	return p.EditComment(ctx, commentID, body)
}

// FetchImage proxies an auth-gated attachment through the named provider.
func (m *Manager) FetchImage(ctx context.Context, provider, url string) (string, []byte, error) {
	p := m.Provider(provider)
	if p == nil {
		return "", nil, fmt.Errorf("%s not connected", provider)
	}
	return p.FetchImage(ctx, url)
}

// Refresh pulls assigned issues from every provider, merges, caches, and notifies.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	type named struct {
		name string
		p    Provider
	}
	provs := make([]named, 0, len(m.providers))
	for name, p := range m.providers {
		provs = append(provs, named{name, p})
	}
	m.mu.Unlock()

	// Fan out: each provider is an independent network round-trip to a different
	// host, so fetch them concurrently and merge the results.
	var merged []Issue
	var firstErr error
	errs := map[string]string{}
	var wg sync.WaitGroup
	var rmu sync.Mutex
	for _, np := range provs {
		wg.Add(1)
		go func(name string, p Provider) {
			defer wg.Done()
			got, err := p.ListAssigned(ctx)
			rmu.Lock()
			defer rmu.Unlock()
			if err != nil {
				// Surface the failure so a "connected but nothing loading" tracker shows WHY
				// (e.g. an expired token or a bad cloud id) via the reconnect pill, instead of
				// silently swallowing it here — the poll discards Refresh's returned error.
				errs[name] = name + ": " + err.Error()
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			merged = append(merged, got...)
		}(np.name, np.p)
	}
	wg.Wait()
	m.mu.Lock()
	m.cache = merged
	cb := m.onUpdate
	// Record fetch failures; clear the error for any provider that fetched cleanly this round.
	for _, np := range provs {
		if msg, bad := errs[np.name]; bad {
			m.authErrors[np.name] = msg
		} else {
			delete(m.authErrors, np.name)
		}
	}
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
				if m.hasProviders() {
					_ = m.Refresh(ctx)
				}
			}
		}
	}()
}

// StartTokenRefresh proactively refreshes OAuth tokens on a ticker (call once). This keeps the
// connection alive (access tokens expire ~hourly; the refresh token would otherwise lapse) AND
// detects when the OAuth has died — a failed refresh records an auth error surfaced to the app so
// it can show a "reconnect" pill.
func (m *Manager) StartTokenRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		m.refreshTokens(ctx) // once at start — catch an already-dead token immediately
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.refreshTokens(ctx)
			}
		}
	}()
}

func (m *Manager) refreshTokens(ctx context.Context) {
	m.mu.Lock()
	provs := make(map[string]Provider, len(m.providers))
	for n, p := range m.providers {
		provs[n] = p
	}
	m.mu.Unlock()

	changed := false
	for name, p := range provs {
		r, ok := p.(TokenRefresher)
		if !ok {
			continue
		}
		err := r.RefreshToken(ctx)
		m.mu.Lock()
		prev, had := m.authErrors[name]
		if err != nil {
			msg := err.Error()
			if !had || prev != msg {
				m.authErrors[name] = msg
				changed = true
			}
		} else if had {
			delete(m.authErrors, name)
			changed = true
		}
		m.mu.Unlock()
	}
	if changed {
		m.mu.Lock()
		cache := m.cache
		m.mu.Unlock()
		if m.onUpdate != nil {
			m.onUpdate(cache) // re-broadcast issues + integration.status (now carrying the auth error)
		}
	}
}

// AuthErrors returns the providers whose fetch/refresh is currently failing (needs reconnect).
func (m *Manager) AuthErrors() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.authErrors))
	for n := range m.authErrors {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AuthErrorDetails returns provider -> the actual failure message, so the app can show WHY a
// connected tracker isn't loading (e.g. "jira: 401 Unauthorized"), not just that it isn't.
func (m *Manager) AuthErrorDetails() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.authErrors) == 0 {
		return nil
	}
	out := make(map[string]string, len(m.authErrors))
	for n, msg := range m.authErrors {
		out[n] = msg
	}
	return out
}

// Disconnect removes a tracker's connection: it drops the live adapter, clears the stored token
// (keeping the OAuth app client_id/secret so reconnecting is one tap), clears any auth error, and
// rebuilds the issue cache from whatever providers remain.
func (m *Manager) Disconnect(ctx context.Context, name string) error {
	m.mu.Lock()
	if _, ok := m.providers[name]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("%q is not connected", name)
	}
	delete(m.providers, name)
	delete(m.authErrors, name)
	switch name {
	case "linear":
		m.cfg.Linear.Token = ""
	case "jira":
		m.cfg.Jira.Token = ""
	}
	cfg := m.cfg
	m.mu.Unlock()
	m.save(cfg)
	// Rebuild the cache from the remaining providers (empty if none), so the disconnected
	// tracker's issues disappear from the board immediately.
	return m.Refresh(ctx)
}

// save writes cfg to disk. It takes cfg by value (not m.cfg) so callers can
// snapshot under the lock and release it before the blocking filesystem syscalls.
func (m *Manager) save(cfg Config) {
	if m.path == "" {
		return
	}
	if dir := filepath.Dir(m.path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if data, err := json.MarshalIndent(cfg, "", "  "); err == nil {
		_ = os.WriteFile(m.path, data, 0o600)
	}
}
