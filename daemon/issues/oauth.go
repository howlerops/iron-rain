package issues

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// Atlassian (Jira) OAuth 2.0 3LO endpoints (developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps).
const (
	jiraAuthorizeURL = "https://auth.atlassian.com/authorize"
	jiraResourcesURL = "https://api.atlassian.com/oauth/token/accessible-resources"
)

// jiraTokenURL is a var (not const) purely so the refresh-race test can point it at a local fake
// IdP — the single-flight guarantee is only worth having if a test can prove it against a server
// that detects rotation reuse.
var jiraTokenURL = "https://auth.atlassian.com/oauth/token"

// linearScopes grant read + write (issue updates + comments) as the authorizing USER.
var linearScopes = []string{"read", "write"}

// jiraScopes: read/write issues + read users, plus offline_access to get a refresh token
// (Atlassian access tokens expire in ~1h, so without this the connection would die hourly).
var jiraScopes = []string{"read:jira-work", "write:jira-work", "read:jira-user", "offline_access"}

// OAuthRedirectURI is the daemon's loopback callback for a provider; register it on that
// provider's OAuth app (e.g. /oauth/linear/callback, /oauth/jira/callback).
func OAuthRedirectURI(addrPort, provider string) string {
	return "http://" + addrPort + "/oauth/" + provider + "/callback"
}

// OAuthStart begins a provider's OAuth flow: returns the authorize URL and records the state.
// Requires the OAuth app's client_id in the saved config (from ~/.oculus/integrations.json).
func (m *Manager) OAuthStart(provider, redirectURI string) (string, error) {
	m.mu.Lock()
	if m.pending == nil {
		m.pending = map[string]string{}
	}
	var clientID string
	switch provider {
	case "linear":
		clientID = m.cfg.Linear.ClientID
	case "jira":
		clientID = m.cfg.Jira.ClientID
	default:
		m.mu.Unlock()
		return "", fmt.Errorf("oauth not supported for %q", provider)
	}
	m.mu.Unlock()
	if clientID == "" {
		return "", fmt.Errorf("no %s OAuth client_id configured (add it in ~/.oculus/integrations.json)", provider)
	}
	state := randState()
	m.mu.Lock()
	m.pending[state] = provider
	m.mu.Unlock()

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	switch provider {
	case "linear":
		q.Set("scope", strings.Join(linearScopes, ","))
		// actor defaults to "user" — act on behalf of the authorizing user. (actor=application
		// requires app-only scopes and rejects the `write` scope.)
		return linearAuthorizeURL + "?" + q.Encode(), nil
	case "jira":
		q.Set("scope", strings.Join(jiraScopes, " ")) // Atlassian uses space-delimited scopes
		q.Set("audience", "api.atlassian.com")
		q.Set("prompt", "consent") // required to (re)issue a refresh token
		return jiraAuthorizeURL + "?" + q.Encode(), nil
	}
	return "", fmt.Errorf("oauth not supported for %q", provider)
}

// OAuthCallback exchanges the authorization code for an access token and connects.
func (m *Manager) OAuthCallback(ctx context.Context, code, state, redirectURI string) error {
	m.mu.Lock()
	provider := m.pending[state]
	delete(m.pending, state)
	m.mu.Unlock()
	if provider == "" {
		return fmt.Errorf("unknown or expired oauth state")
	}
	switch provider {
	case "linear":
		return m.linearCallback(ctx, code, redirectURI)
	case "jira":
		return m.jiraCallback(ctx, code, redirectURI)
	}
	return fmt.Errorf("oauth not supported for %q", provider)
}

func (m *Manager) linearCallback(ctx context.Context, code, redirectURI string) error {
	m.mu.Lock()
	clientID, clientSecret := m.cfg.Linear.ClientID, m.cfg.Linear.ClientSecret
	m.mu.Unlock()
	tok, err := exchangeCode(ctx, linearTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	})
	if err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("linear token exchange returned no access_token")
	}
	return m.Connect(ctx, "linear", tok.AccessToken)
}

func (m *Manager) jiraCallback(ctx context.Context, code, redirectURI string) error {
	m.mu.Lock()
	clientID, clientSecret := m.cfg.Jira.ClientID, m.cfg.Jira.ClientSecret
	m.mu.Unlock()
	tok, err := exchangeCode(ctx, jiraTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	})
	if err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("jira token exchange returned no access_token")
	}
	// Resolve the Jira site (cloudid) the token can access — OAuth API calls go to
	// https://api.atlassian.com/ex/jira/{cloudid}/rest/... rather than the site URL.
	cloudID, ambiguous, err := jiraCloudID(ctx, tok.AccessToken)
	if err != nil {
		return err
	}
	m.setJiraSiteAmbiguous(ambiguous)
	// Persisted token format: oauth|cloudid|access|refresh (see newAdapter).
	composite := strings.Join([]string{"oauth", cloudID, tok.AccessToken, tok.RefreshToken}, "|")
	return m.Connect(ctx, "jira", composite)
}

// oauthToken is the subset of a token response we use.
type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// exchangeCode POSTs an OAuth token request (form-encoded) and decodes the token response.
func exchangeCode(ctx context.Context, tokenURL string, form url.Values) (oauthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second} // never let a stuck token exchange hang
	resp, err := client.Do(req)
	if err != nil {
		return oauthToken{}, err
	}
	defer drainClose(resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauthToken{}, err
	}
	// A non-2xx from the token endpoint used to be swallowed: the error body ("invalid_grant") isn't
	// an oauthToken, so decoding yielded an EMPTY token and the caller reported the useless "no
	// access_token". The status + OAuth error code are the diagnosis — a revoked refresh token reads
	// completely differently from a network blip, and downstream messages depend on seeing it.
	if resp.StatusCode/100 != 2 {
		var oe struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &oe)
		msg := oe.Error
		if oe.Desc != "" {
			msg += ": " + oe.Desc
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return oauthToken{}, fmt.Errorf("token endpoint HTTP %d: %s", resp.StatusCode, msg)
	}
	var tok oauthToken
	if err := json.Unmarshal(body, &tok); err != nil {
		return oauthToken{}, err
	}
	return tok, nil
}

// jiraCloudID returns the first Jira site (cloudid) the access token can reach.
// JiraSiteInfo is one Atlassian site (cloud) the OAuth token can access.
type JiraSiteInfo struct {
	ID   string `json:"id"` // cloud id (routes /ex/jira/{id})
	Name string `json:"name"`
	URL  string `json:"url"`
}

// jiraAccessibleSites lists every Atlassian site the token can reach. An org with more than one
// site is exactly why picking [0] is wrong — the caller must let the user choose.
func jiraAccessibleSites(ctx context.Context, accessToken string) ([]JiraSiteInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jiraResourcesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	var resources []JiraSiteInfo
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return nil, err
	}
	return resources, nil
}

// jiraCloudID picks the site to route API calls at, and reports whether that pick was a GUESS.
//
// Atlassian returns no indication of which site the user chose on the consent screen — only the full
// list the token can reach, unordered. With one site there is nothing to get wrong. With several,
// `sites[0]` is a coin flip, and losing it means the board quietly shows the wrong project. The
// choice still has to be made (a connection must route somewhere), but the caller now learns it was
// arbitrary and can ask.
func jiraCloudID(ctx context.Context, accessToken string) (id string, ambiguous bool, err error) {
	sites, err := jiraAccessibleSites(ctx, accessToken)
	if err != nil {
		return "", false, err
	}
	if len(sites) == 0 {
		return "", false, fmt.Errorf("jira: the token has access to no sites (grant the app access to a Jira site)")
	}
	return sites[0].ID, len(sites) > 1, nil
}

// refreshJiraToken exchanges a refresh token for a fresh access (+ rotated refresh) token.
// Atlassian rotates refresh tokens, so the caller MUST persist the new refresh token.
func refreshJiraToken(ctx context.Context, clientID, clientSecret, refreshToken string) (oauthToken, error) {
	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return oauthToken{}, fmt.Errorf("jira: cannot refresh (missing client creds or refresh token)")
	}
	return exchangeCode(ctx, jiraTokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	})
}

func randState() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
