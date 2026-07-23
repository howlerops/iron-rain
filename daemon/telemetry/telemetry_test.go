package telemetry

import (
	"os"
	"strings"
	"testing"
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
