package issues

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManager_LoadsTokenOnStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "integrations.json")
	_ = os.WriteFile(path, []byte(`{"linear":{"token":"lin_abc"}}`), 0o600)

	m := NewManager(path, nil)
	if got := m.Connected(); len(got) != 1 || got[0] != "linear" {
		t.Fatalf("connected = %v, want [linear]", got)
	}
}

func TestManager_ReconnectsSavedJira(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "integrations.json")
	_ = os.WriteFile(path, []byte(`{"jira":{"token":"https://x.atlassian.net|me@x.com|apitok"}}`), 0o600)

	m := NewManager(path, nil)
	if got := m.Connected(); len(got) != 1 || got[0] != "jira" {
		t.Fatalf("connected = %v, want [jira]", got)
	}
}

func TestManager_OAuthStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "integrations.json")
	_ = os.WriteFile(path, []byte(`{"linear":{"client_id":"cid123"}}`), 0o600)

	m := NewManager(path, nil)
	redirect := "http://127.0.0.1:6000/oauth/linear/callback"
	url1, err := m.OAuthStart("linear", redirect)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"linear.app/oauth/authorize", "client_id=cid123", "response_type=code", "state=", "scope=read%2Cwrite"} {
		if !strings.Contains(url1, want) {
			t.Errorf("authorize URL %q missing %q", url1, want)
		}
	}
	// User actor (default) — actor=application mixed with the write scope is what Linear
	// rejects ("scopes not valid for this actor mode"), so it must NOT be present.
	if strings.Contains(url1, "actor=application") {
		t.Errorf("authorize URL must not use actor=application: %q", url1)
	}
	// A second start yields a fresh state.
	url2, _ := m.OAuthStart("linear", redirect)
	if state(url1) == state(url2) {
		t.Error("expected a fresh oauth state per start")
	}
}

func TestManager_OAuthStart_NoClientID(t *testing.T) {
	m := NewManager("", nil)
	if _, err := m.OAuthStart("linear", "http://x/cb"); err == nil {
		t.Fatal("expected error without a client_id")
	}
}

func state(u string) string {
	i := strings.Index(u, "state=")
	if i < 0 {
		return ""
	}
	s := u[i+len("state="):]
	if j := strings.IndexByte(s, '&'); j >= 0 {
		s = s[:j]
	}
	return s
}
