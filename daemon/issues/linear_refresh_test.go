package issues

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLinearImplementsTokenRefresher pins the gap that caused daily Linear re-auth: the Manager's
// refresh loop type-asserts each provider to TokenRefresher, and Linear did not implement it, so it
// was skipped in silence and simply expired.
func TestLinearImplementsTokenRefresher(t *testing.T) {
	var p Provider = NewLinear("tok")
	if _, ok := p.(TokenRefresher); !ok {
		t.Fatal("Linear must implement TokenRefresher or the refresh loop skips it entirely")
	}
}

// A bare (non-OAuth) token has nothing to refresh and must not report an error — a personal API key
// does not expire, and a spurious auth error would show a "reconnect" pill on a healthy connection.
func TestLinearBareTokenRefreshIsNoop(t *testing.T) {
	if err := NewLinear("personal-api-key").RefreshToken(t.Context()); err != nil {
		t.Fatalf("bare token refresh should be a no-op, got %v", err)
	}
}

// A lapsed access token must renew itself instead of surfacing as "reconnect Linear".
//
// Refresh only ever ran on the Manager's 40-minute timer, so a call landing after the token expired
// and before the next tick failed with HTTP 401 — the board emptied and the app asked the user to
// re-authenticate a connection the daemon could have renewed on its own. Jira already refreshed on
// 401 and retried; Linear did not, which is why it was the one that kept asking.
func TestLinearRetriesOnceAfterRefreshingA401(t *testing.T) {
	var apiCalls, refreshes int
	var lastAuth []string

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token") {
			refreshes++
			_ = r.ParseForm()
			if r.Form.Get("refresh_token") != "refresh-1" {
				t.Errorf("refresh sent the wrong token: %q", r.Form.Get("refresh_token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fresh-token","refresh_token":"refresh-2"}`))
			return
		}
		apiCalls++
		lastAuth = append(lastAuth, r.Header.Get("Authorization"))
		if apiCalls == 1 {
			w.WriteHeader(http.StatusUnauthorized) // the token lapsed between refresh ticks
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer api.Close()

	l := NewLinear("stale-token")
	l.endpoint = api.URL
	var savedAccess, savedRefresh string
	l.SetOAuth("refresh-1", "cid", "sec", func(a, r string) { savedAccess, savedRefresh = a, r })
	restore := linearTokenURL
	linearTokenURL = api.URL + "/token"
	defer func() { linearTokenURL = restore }()

	var out struct {
		OK bool `json:"ok"`
	}
	if err := l.gql(t.Context(), "query{ok}", nil, &out); err != nil {
		t.Fatalf("the call should have healed itself, got %v", err)
	}
	if !out.OK {
		t.Fatal("the retried call's data was not decoded")
	}
	if refreshes != 1 {
		t.Fatalf("expected exactly one refresh (it is single-flighted), got %d", refreshes)
	}
	if apiCalls != 2 {
		t.Fatalf("expected the original call plus ONE retry, got %d", apiCalls)
	}
	if len(lastAuth) != 2 || !strings.Contains(lastAuth[1], "fresh-token") {
		t.Fatalf("the retry did not use the refreshed token: %v", lastAuth)
	}
	// The rotated refresh token has to be persisted, or the NEXT refresh reuses a spent one and
	// Linear revokes the whole family.
	if savedAccess != "fresh-token" || savedRefresh != "refresh-2" {
		t.Fatalf("rotated credentials were not handed back for persistence: %q %q", savedAccess, savedRefresh)
	}
}

// A 401 on a connection with nothing to refresh must say WHY, not retry pointlessly.
//
// Linear access tokens last 24 hours. Connections made before this daemon persisted the refresh
// token kept only the access token, so they expire every single day and no retry can renew them —
// the "why does Linear keep asking me to log in" loop. The message has to name that and say the
// reconnect is one-time, or the user does it again tomorrow.
func TestLinearLegacyOAuthTokenExplainsItself(t *testing.T) {
	var calls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()

	l := NewLinear("lin_oauth_legacyvalue") // bare access token: no SetOAuth, nothing to refresh with
	l.endpoint = api.URL

	err := l.gql(t.Context(), "query{ok}", nil, nil)
	if err == nil {
		t.Fatal("expected an error for a token that cannot be renewed")
	}
	if !strings.Contains(err.Error(), "reconnect once") {
		t.Fatalf("the error must explain the one-time reconnect, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("must not retry when there is nothing to refresh with, got %d calls", calls)
	}
}

// A revoked personal API key is a different situation and must read differently.
func TestLinearRevokedAPIKeySaysRevoked(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()
	l := NewLinear("lin_api_personalkey")
	l.endpoint = api.URL
	err := l.gql(t.Context(), "query{ok}", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a rejected API key should say it may have been revoked, got: %v", err)
	}
}
