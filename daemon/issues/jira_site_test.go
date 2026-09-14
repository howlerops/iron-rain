package issues

import (
	"path/filepath"
	"strings"
	"testing"
)

// A token refresh from a superseded adapter must not rewrite the live site's connection.
//
// The onRefresh closure captures the cloud id its adapter was built with. A multi-site org can
// switch sites (jira.set_site), which builds a NEW adapter against a different cloud id while the
// old one may still have a refresh in flight. That late refresh used to rewrite the persisted token
// with the OLD site's cloud id and the NEW refresh token, doing two things at once: pointing the
// daemon back at the site the user just left, and stranding the live adapter with a refresh token
// that has been rotated out from under it. The next refresh gets invalid_grant and polling
// suspends — "connected but no tickets", against the wrong site.
func TestALateRefreshFromTheOldSiteDoesNotClobberTheNewOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integrations.json")
	m := NewManager(path, func([]Issue) {})

	// Connected to site A, then the user switches to site B.
	m.mu.Lock()
	m.cfg.Jira.Token = "oauth|site-A|accessA|refreshA"
	m.mu.Unlock()
	stale := jiraRefreshOf(t, m, "oauth|site-A|accessA|refreshA")

	m.mu.Lock()
	m.cfg.Jira.Token = "oauth|site-B|accessB|refreshB" // set_site swapped the live connection
	m.mu.Unlock()

	// Site A's in-flight refresh lands late.
	stale("accessA2", "refreshA2")

	m.mu.Lock()
	got := m.cfg.Jira.Token
	m.mu.Unlock()
	if !strings.HasPrefix(got, "oauth|site-B|") {
		t.Fatalf("the stale refresh rewrote the live connection: token is now %q.\n\n"+
			"The daemon is pointed back at the site the user left, and the live adapter's refresh token "+
			"has been rotated out from under it — the next refresh gets invalid_grant and polling stops.",
			redact(got))
	}
	if !strings.Contains(got, "refreshB") {
		t.Errorf("the live site's refresh token was replaced: %q", redact(got))
	}
}

// The ordinary case must still persist: a refresh from the LIVE adapter has to be written, or the
// next one presents a spent token.
func TestARefreshFromTheLiveSiteIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integrations.json")
	m := NewManager(path, func([]Issue) {})
	m.mu.Lock()
	m.cfg.Jira.Token = "oauth|site-A|accessA|refreshA"
	m.mu.Unlock()

	live := jiraRefreshOf(t, m, "oauth|site-A|accessA|refreshA")
	live("accessA2", "refreshA2")

	m.mu.Lock()
	got := m.cfg.Jira.Token
	m.mu.Unlock()
	if got != "oauth|site-A|accessA2|refreshA2" {
		t.Fatalf("the live adapter's rotated pair was not persisted: %q — the next refresh presents a "+
			"token that has already been spent", redact(got))
	}
}

// jiraRefreshOf builds the adapter for a token and hands back the persist-on-refresh callback it was
// wired with — the same closure a real 401-driven refresh would invoke.
func jiraRefreshOf(t *testing.T, m *Manager, token string) func(access, refresh string) {
	t.Helper()
	p, err := m.newAdapter("jira", token)
	if err != nil {
		t.Fatalf("newAdapter(%s): %v", redact(token), err)
	}
	j, ok := p.(*Jira)
	if !ok || j.onRefresh == nil {
		t.Fatalf("the jira adapter was built without a refresh callback")
	}
	return j.onRefresh
}

func redact(token string) string {
	parts := strings.SplitN(token, "|", 4)
	if len(parts) != 4 {
		return token
	}
	return strings.Join([]string{parts[0], parts[1], "…", "…"}, "|")
}
