// Package quota proactively reads an account's remaining rate-limit / quota straight from the
// provider's API (using the account's API key), rather than waiting for the agent to hit a limit
// mid-run (that reactive signal lives in package ratelimit). Both Anthropic and OpenAI return
// rate-limit headers on every API response; a cheap probe (Anthropic count_tokens — free; OpenAI
// models list) reads them so the app can show "requests/tokens remaining, resets in N" per account.
//
// Only API-key accounts can be probed; subscription logins (e.g. the Claude app) expose no
// key-based quota endpoint, so those return ErrNoKey — honestly surfaced as "not available".
package quota

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNoKey means the account has no probeable API key (e.g. a subscription login).
var ErrNoKey = errors.New("account has no API key to probe")

// Quota is the parsed rate-limit snapshot. -1 means unknown/not reported.
type Quota struct {
	Provider          string    `json:"provider"`
	RequestsRemaining int       `json:"requests_remaining"`
	TokensRemaining   int       `json:"tokens_remaining"`
	ResetAt           time.Time `json:"reset_at,omitempty"`
	Available         bool      `json:"available"` // false when no key / probe failed
	Note              string    `json:"note,omitempty"`
}

// Prober performs the probe. HTTP + base URLs are injectable for tests.
type Prober struct {
	HTTP          *http.Client
	AnthropicBase string
	OpenAIBase    string
}

// New returns a Prober with production defaults.
func New() *Prober {
	return &Prober{
		HTTP:          &http.Client{Timeout: 12 * time.Second},
		AnthropicBase: "https://api.anthropic.com",
		OpenAIBase:    "https://api.openai.com",
	}
}

// keyFor picks the API key from an account's env by well-known names for the provider family.
func keyFor(provider string, env map[string]string) string {
	p := strings.ToLower(provider)
	anthropic := strings.Contains(p, "claude") || strings.Contains(p, "anthropic")
	openai := strings.Contains(p, "codex") || strings.Contains(p, "openai") || strings.Contains(p, "gpt")
	// Try provider-specific names first, then generic.
	order := []string{}
	if anthropic {
		order = append(order, "ANTHROPIC_API_KEY", "CLAUDE_API_KEY")
	}
	if openai {
		order = append(order, "OPENAI_API_KEY")
	}
	order = append(order, "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "API_KEY")
	for _, k := range order {
		if v := strings.TrimSpace(env[k]); v != "" {
			return v
		}
	}
	return ""
}

// family classifies a provider into "anthropic" or "openai" for endpoint selection.
func family(provider string, env map[string]string) string {
	p := strings.ToLower(provider)
	if strings.Contains(p, "claude") || strings.Contains(p, "anthropic") || env["ANTHROPIC_API_KEY"] != "" {
		return "anthropic"
	}
	if strings.Contains(p, "codex") || strings.Contains(p, "openai") || strings.Contains(p, "gpt") || env["OPENAI_API_KEY"] != "" {
		return "openai"
	}
	return ""
}

// Probe reads the account's remaining quota. Returns ErrNoKey when there's no probeable key.
func (p *Prober) Probe(ctx context.Context, provider string, env map[string]string) (*Quota, error) {
	key := keyFor(provider, env)
	if key == "" {
		return &Quota{Provider: provider, Available: false, Note: "no API key on this account"}, ErrNoKey
	}
	switch family(provider, env) {
	case "anthropic":
		return p.probeAnthropic(ctx, provider, key)
	case "openai":
		return p.probeOpenAI(ctx, provider, key)
	default:
		return &Quota{Provider: provider, Available: false, Note: "unknown provider family"}, fmt.Errorf("unknown provider family for %q", provider)
	}
}

func (p *Prober) probeAnthropic(ctx context.Context, provider, key string) (*Quota, error) {
	// count_tokens is free and returns the same anthropic-ratelimit-* headers as the Messages API.
	body := []byte(`{"model":"claude-3-5-haiku-latest","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.AnthropicBase+"/v1/messages/count_tokens", bytes.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return &Quota{Provider: provider, Available: false, Note: err.Error()}, err
	}
	defer resp.Body.Close()
	q := &Quota{
		Provider:          provider,
		Available:         true,
		RequestsRemaining: headerInt(resp.Header, "anthropic-ratelimit-requests-remaining"),
		TokensRemaining:   headerInt(resp.Header, "anthropic-ratelimit-tokens-remaining"),
		ResetAt:           headerTime(resp.Header, "anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-reset"),
	}
	if resp.StatusCode == http.StatusUnauthorized {
		q.Available = false
		q.Note = "invalid API key"
	}
	return q, nil
}

func (p *Prober) probeOpenAI(ctx context.Context, provider, key string) (*Quota, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.OpenAIBase+"/v1/models", nil)
	req.Header.Set("authorization", "Bearer "+key)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return &Quota{Provider: provider, Available: false, Note: err.Error()}, err
	}
	defer resp.Body.Close()
	q := &Quota{
		Provider:          provider,
		Available:         true,
		RequestsRemaining: headerInt(resp.Header, "x-ratelimit-remaining-requests"),
		TokensRemaining:   headerInt(resp.Header, "x-ratelimit-remaining-tokens"),
		ResetAt:           headerDuration(resp.Header, "x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"),
	}
	if resp.StatusCode == http.StatusUnauthorized {
		q.Available = false
		q.Note = "invalid API key"
	}
	return q, nil
}

// headerInt reads an integer header, or -1 if absent/unparseable.
func headerInt(h http.Header, name string) int {
	v := h.Get(name)
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return -1
	}
	return n
}

// headerTime reads the first present RFC3339 reset header (Anthropic uses absolute timestamps).
func headerTime(h http.Header, names ...string) time.Time {
	for _, name := range names {
		if v := h.Get(name); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// headerDuration reads the first present reset header as a duration from now (OpenAI uses "6m0s" or
// bare seconds) and returns the absolute reset time.
func headerDuration(h http.Header, names ...string) time.Time {
	for _, name := range names {
		v := strings.TrimSpace(h.Get(name))
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err == nil {
			return timeNow().Add(d)
		}
		if secs, err := strconv.Atoi(v); err == nil {
			return timeNow().Add(time.Duration(secs) * time.Second)
		}
	}
	return time.Time{}
}

// timeNow is overridable in tests.
var timeNow = time.Now
