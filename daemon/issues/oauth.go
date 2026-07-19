package issues

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Linear OAuth endpoints (docs.linear.app/oauth-2-0-authentication).
const (
	linearAuthorizeURL = "https://linear.app/oauth/authorize"
	linearTokenURL     = "https://api.linear.app/oauth/token"
)

// linearScopes grant read + write (issue updates + comments) as the authorizing USER.
// NOTE: the app:assignable/app:mentionable scopes + actor=application (agent identity) are
// mutually exclusive with the broad `write` scope — mixing them yields Linear's "scopes
// requested are not valid for this actor mode" error. User-actor read+write is the reliable
// path for a personal connection; app-actor would require the Linear OAuth app to be
// configured as an application with assignable/mentionable enabled.
var linearScopes = []string{"read", "write"}

// OAuthRedirectURI is the daemon's loopback callback; register it on the Linear OAuth app.
func OAuthRedirectURI(addrPort string) string {
	return "http://" + addrPort + "/oauth/linear/callback"
}

// OAuthStart begins a Linear OAuth flow: returns the authorize URL and records the state.
// Requires the OAuth app's client_id in the saved config (from ~/.oculus/integrations.json).
func (m *Manager) OAuthStart(provider, redirectURI string) (string, error) {
	if provider != "linear" {
		return "", fmt.Errorf("oauth not supported for %q", provider)
	}
	m.mu.Lock()
	clientID := m.cfg.Linear.ClientID
	if m.pending == nil {
		m.pending = map[string]string{}
	}
	m.mu.Unlock()
	if clientID == "" {
		return "", fmt.Errorf("no Linear OAuth client_id configured")
	}
	state := randState()
	m.mu.Lock()
	m.pending[state] = provider
	m.mu.Unlock()

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(linearScopes, ","))
	q.Set("state", state)
	// actor defaults to "user" — act on behalf of the authorizing user. (actor=application
	// requires app-only scopes and rejects the `write` scope; see linearScopes note.)
	return linearAuthorizeURL + "?" + q.Encode(), nil
}

// OAuthCallback exchanges the authorization code for an access token and connects.
func (m *Manager) OAuthCallback(ctx context.Context, code, state, redirectURI string) error {
	m.mu.Lock()
	provider := m.pending[state]
	delete(m.pending, state)
	clientID := m.cfg.Linear.ClientID
	clientSecret := m.cfg.Linear.ClientSecret
	m.mu.Unlock()
	if provider == "" {
		return fmt.Errorf("unknown or expired oauth state")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second} // never let a stuck token exchange hang
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("linear token exchange returned no access_token (HTTP %s)", resp.Status)
	}
	return m.Connect(ctx, provider, tok.AccessToken)
}

func randState() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
