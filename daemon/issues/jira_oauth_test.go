package issues

import (
	"strings"
	"testing"
)

func TestNewAdapter_JiraOAuth(t *testing.T) {
	m := &Manager{}
	m.cfg.Jira.ClientID = "cid"
	m.cfg.Jira.ClientSecret = "sec"
	p, err := m.newAdapter("jira", "oauth|cloud-123|access-abc|refresh-xyz")
	if err != nil {
		t.Fatalf("newAdapter: %v", err)
	}
	j, ok := p.(*Jira)
	if !ok {
		t.Fatalf("not *Jira: %T", p)
	}
	if !j.oauth {
		t.Error("expected oauth mode")
	}
	if j.base != "https://api.atlassian.com/ex/jira/cloud-123" {
		t.Errorf("base = %q", j.base)
	}
	if j.auth != "Bearer access-abc" {
		t.Errorf("auth = %q", j.auth)
	}
	if j.refreshToken != "refresh-xyz" || j.clientID != "cid" || j.clientSecret != "sec" {
		t.Errorf("creds not threaded: %+v", j)
	}
}

func TestNewAdapter_JiraBasic(t *testing.T) {
	m := &Manager{}
	p, err := m.newAdapter("jira", "https://site.atlassian.net|me@x.com|tok")
	if err != nil {
		t.Fatalf("newAdapter: %v", err)
	}
	j := p.(*Jira)
	if j.oauth {
		t.Error("should be basic mode")
	}
	if j.base != "https://site.atlassian.net" || !strings.HasPrefix(j.auth, "Basic ") {
		t.Errorf("basic adapter wrong: base=%q auth=%q", j.base, j.auth)
	}
}

func TestOAuthStart_Jira(t *testing.T) {
	m := &Manager{}
	m.cfg.Jira.ClientID = "cid"
	u, err := m.OAuthStart("jira", "http://127.0.0.1:6900/oauth/jira/callback")
	if err != nil {
		t.Fatalf("OAuthStart: %v", err)
	}
	for _, want := range []string{
		"auth.atlassian.com/authorize",
		"client_id=cid",
		"audience=api.atlassian.com",
		"offline_access", // refresh-token scope
		"prompt=consent",
		"127.0.0.1%3A6900%2Foauth%2Fjira%2Fcallback", // redirect_uri, url-encoded
	} {
		if !strings.Contains(u, want) {
			t.Errorf("authorize URL missing %q:\n%s", want, u)
		}
	}
}

func TestOAuthStart_MissingClientID(t *testing.T) {
	m := &Manager{}
	if _, err := m.OAuthStart("jira", "http://x/cb"); err == nil {
		t.Error("expected error when no jira client_id is configured")
	}
}
