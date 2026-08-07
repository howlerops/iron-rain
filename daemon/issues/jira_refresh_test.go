package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentRefreshIsSingleFlight reproduces the daily re-auth bug. Atlassian ROTATES refresh
// tokens and treats reuse of a rotated one as theft, revoking the whole token family. Before the
// fix, the 40-minute cron and a 401-triggered refresh (or several parallel calls expiring together)
// each presented the SAME refresh token: the winner rotated it, the loser's re-presentation
// poisoned the family, and the user re-authed daily. Exactly ONE exchange may run; waiters must
// reuse its result.
func TestConcurrentRefreshIsSingleFlight(t *testing.T) {
	var exchanges atomic.Int32
	var mu sync.Mutex
	seen := map[string]bool{} // refresh tokens presented to the "IdP"

	// The exchange is HELD OPEN until the test releases it. Without this the first refresh could
	// complete before the other goroutines had even read their generation counter, and each of those
	// would then correctly start its own exchange — which is right behaviour, but means the test was
	// asserting on scheduler luck. It passed locally for weeks and failed on a loaded CI runner.
	release := make(chan struct{})
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_ = r.ParseForm()
		rt := r.Form.Get("refresh_token")
		mu.Lock()
		if seen[rt] {
			// Rotation reuse — the real Atlassian would revoke the family here.
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			t.Errorf("a rotated refresh token was re-presented (%q) — the family would be revoked", rt)
			return
		}
		seen[rt] = true
		n := exchanges.Add(1)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-" + rt,
			"refresh_token": "rotated-" + itoaTest(int(n)),
		})
	}))
	defer idp.Close()

	restore := jiraTokenURL
	jiraTokenURL = idp.URL
	defer func() { jiraTokenURL = restore }()

	var persisted atomic.Int32
	j := NewJiraOAuth("cloud", "old-access", "rt-original", "cid", "csec",
		func(access, refresh string) { persisted.Add(1) })

	// The real collision: cron + 401 handler + parallel board calls, all at once.
	//
	// `entered` makes them genuinely concurrent rather than merely started in a loop: every goroutine
	// announces itself, all wait for the last, and only then is the held exchange released. That is
	// the collision this test exists to describe.
	var wg sync.WaitGroup
	var entered sync.WaitGroup
	entered.Add(8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered.Done()
			<-start
			if err := j.refresh(context.Background()); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	entered.Wait()
	close(start)
	// Give every goroutine time to read its generation and queue on refreshMu before the one holding
	// the exchange is allowed to finish and bump that generation.
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := exchanges.Load(); got != 1 {
		t.Fatalf("8 concurrent refreshes performed %d exchanges, want exactly 1", got)
	}
	// And a LATER refresh (next hour's expiry) presents the ROTATED token, not the original.
	if err := j.refresh(context.Background()); err != nil {
		t.Fatalf("second-generation refresh: %v", err)
	}
	if got := exchanges.Load(); got != 2 {
		t.Fatalf("expected the later refresh to run a real exchange, total %d", got)
	}
}

// TestDeadRefreshTokenSaysReconnect: a revoked family is a "reconnect Jira" situation, not a
// transient error — the message drives the reconnect pill, so it must say what to do.
func TestDeadRefreshTokenSaysReconnect(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer idp.Close()
	restore := jiraTokenURL
	jiraTokenURL = idp.URL
	defer func() { jiraTokenURL = restore }()

	j := NewJiraOAuth("cloud", "a", "dead-rt", "cid", "csec", nil)
	err := j.refresh(context.Background())
	if err == nil {
		t.Fatal("a dead refresh token must error")
	}
	if want := "reconnect Jira"; !containsFold(err.Error(), want) {
		t.Errorf("error should tell the user what to do, got %q", err)
	}
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			ok := true
			for k := 0; k < len(sub); k++ {
				a, b := s[i+k], sub[k]
				if a >= 'A' && a <= 'Z' {
					a += 32
				}
				if b >= 'A' && b <= 'Z' {
					b += 32
				}
				if a != b {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	})()
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
