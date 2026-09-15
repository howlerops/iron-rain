package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestScrubRedactsPaths is the privacy guardrail: no absolute path or home-dir string may survive
// scrubbing, while the failure shape (the recognizable message) is preserved.
func TestScrubRedactsPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		in       string
		mustHave string   // the failure shape we still want
		mustNot  []string // fragments that must NOT leak
	}{
		{
			in:       "workspace setup failed: git worktree add: /Users/jacob/code/secret-repo/.git/worktrees/x locked",
			mustHave: "git worktree add",
			mustNot:  []string{"secret-repo", "/Users/jacob/code"},
		},
		{
			in:       "open " + home + "/.oculus/creds.json: permission denied",
			mustHave: "permission denied",
			mustNot:  []string{home},
		},
	}
	for _, c := range cases {
		got := scrub(c.in)
		if !strings.Contains(got, c.mustHave) {
			t.Errorf("scrub(%q) = %q; want it to contain %q", c.in, got, c.mustHave)
		}
		for _, bad := range c.mustNot {
			if bad != "" && strings.Contains(got, bad) {
				t.Errorf("scrub(%q) = %q; LEAKED %q", c.in, got, bad)
			}
		}
	}
}

func TestScrubTruncates(t *testing.T) {
	got := scrub(strings.Repeat("x", 400))
	if len([]rune(got)) > 210 {
		t.Errorf("scrub did not truncate: len=%d", len([]rune(got)))
	}
}

// The stated contract is "no repo/user/path detail leaks". It was enforced for PATHS only.
//
// Provider errors quote the thing that failed — a branch name, a commit subject, a shell command,
// a model's own words — and quoted spans survive every path rule. That is precisely the user content
// the contract promises not to send. The failure SHAPE, which is what makes these reports useful,
// is the words around the quotes and is preserved.
func TestScrubRemovesQuotedUserContent(t *testing.T) {
	for _, tc := range []struct{ name, in, mustNotContain string }{
		{"a branch name", `git worktree add failed: branch "feature/acme-billing-secret" already exists`, "acme-billing"},
		{"a commit subject", `commit rejected: 'fix: remove the Contoso API key'`, "Contoso"},
		{"a shell command", "command failed: `curl -H \"Authorization: Bearer sk-live-123\" https://x`", "sk-live-123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scrub(tc.in)
			if strings.Contains(got, tc.mustNotContain) {
				t.Errorf("quoted user content survived scrubbing: %q", got)
			}
			if !strings.Contains(got, "[redacted]") {
				t.Errorf("expected a redaction marker, got %q", got)
			}
		})
	}
}

// The shape must survive, or the telemetry stops being worth collecting.
func TestScrubKeepsTheFailureShape(t *testing.T) {
	got := scrub(`git worktree add timed out after 30s for "my-branch"`)
	for _, want := range []string{"git worktree add", "timed out"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure shape was lost: %q (missing %q)", got, want)
		}
	}
}

// Every batch carries the ingest key, unconditionally.
//
// Unconditional is what makes the rollout order irrelevant: an older Worker ignores the header, a
// newer one with INGEST_KEY set requires it, and neither ordering drops a daemon. See the Worker's
// own comment for what this is and is not — it ships in a binary, so it closes the drive-by path
// rather than authenticating anyone.
func TestEveryBatchCarriesTheIngestKey(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(filepath.Join(t.TempDir(), "telemetry.json"), "test")
	c.endpoint = srv.URL
	c.Record("session.create", "opencode", time.Millisecond, nil)
	c.flush(context.Background())

	select {
	case auth := <-got:
		if auth != "Bearer "+IngestKey {
			t.Fatalf("Authorization = %q, want %q.\n\nThe Worker refuses a batch with no key once "+
				"INGEST_KEY is configured, so a daemon that does not send one stops reporting the "+
				"moment that secret is set.", auth, "Bearer "+IngestKey)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no batch was sent at all")
	}
}
