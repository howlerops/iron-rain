package store

import (
	"path/filepath"
	"testing"
	"time"
)

func usageStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestUsageSurvivesTheSession is the whole point: the live meter died with the session, so a
// finished job's spend was unrecoverable.
func TestUsageSurvivesTheSession(t *testing.T) {
	s := usageStore(t)
	now := time.Now().Unix()
	must := func(e UsageEvent) {
		t.Helper()
		if err := s.AppendUsage(e); err != nil {
			t.Fatal(err)
		}
	}
	must(UsageEvent{TS: now, SessionID: "a", Provider: "opencode", Model: "sonnet", InTokens: 100, OutTokens: 50, CostUSD: 0.10})
	must(UsageEvent{TS: now, SessionID: "a", Provider: "opencode", Model: "sonnet", InTokens: 200, OutTokens: 20, CostUSD: 0.20})
	must(UsageEvent{TS: now, SessionID: "b", Provider: "claude-code", Model: "opus", InTokens: 10, OutTokens: 5, CostUSD: 1.00})

	total, err := s.UsageTotal(now - 60)
	if err != nil {
		t.Fatal(err)
	}
	if total.InTokens != 310 || total.OutTokens != 75 {
		t.Errorf("tokens = %d/%d, want 310/75", total.InTokens, total.OutTokens)
	}
	if total.CostUSD < 1.29 || total.CostUSD > 1.31 {
		t.Errorf("cost = %f, want ~1.30", total.CostUSD)
	}

	byProvider, err := s.UsageSince(now-60, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if len(byProvider) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(byProvider))
	}
	// Ordered by cost — the expensive one first is what a spend view wants.
	if byProvider[0].Key != "claude-code" {
		t.Errorf("heaviest spender should lead, got %q", byProvider[0].Key)
	}

	// Older usage is excluded by the cutoff, which is what makes "today" mean today.
	must(UsageEvent{TS: now - 86400*3, SessionID: "old", Provider: "opencode", CostUSD: 99})
	recent, _ := s.UsageTotal(now - 60)
	if recent.CostUSD > 2 {
		t.Errorf("a 3-day-old event leaked into the recent total: %f", recent.CostUSD)
	}
}

// TestEmptyUsageEventsAreDropped: providers emit frames carrying only a cost or only tokens, and
// all-zero rows would inflate event counts without adding information.
func TestEmptyUsageEventsAreDropped(t *testing.T) {
	s := usageStore(t)
	now := time.Now().Unix()
	if err := s.AppendUsage(UsageEvent{TS: now, SessionID: "a", Provider: "opencode"}); err != nil {
		t.Fatal(err)
	}
	total, _ := s.UsageTotal(now - 60)
	if total.Events != 0 {
		t.Errorf("an all-zero event must not be stored, got %d", total.Events)
	}
}

// TestFirstUsageAnchorsTheWindow: a rolling window resets relative to when usage BEGAN, so the
// anchor has to come from the data rather than the clock.
func TestFirstUsageAnchorsTheWindow(t *testing.T) {
	s := usageStore(t)
	now := time.Now().Unix()
	// Usage two hours ago, then more recently.
	_ = s.AppendUsage(UsageEvent{TS: now - 7200, SessionID: "a", Provider: "opencode", CostUSD: 0.5})
	_ = s.AppendUsage(UsageEvent{TS: now - 60, SessionID: "a", Provider: "opencode", CostUSD: 0.5})

	first, err := s.FirstUsageSince(now - 5*3600)
	if err != nil {
		t.Fatal(err)
	}
	if first != now-7200 {
		t.Errorf("window anchor = %d, want the EARLIEST usage in range (%d)", first, now-7200)
	}
	// Outside any usage, there's no window to reset.
	if f, _ := s.FirstUsageSince(now + 3600); f != 0 {
		t.Errorf("no usage in range must report no anchor, got %d", f)
	}
}

func TestPruneUsage(t *testing.T) {
	s := usageStore(t)
	now := time.Now().Unix()
	_ = s.AppendUsage(UsageEvent{TS: now - 86400*40, SessionID: "old", Provider: "p", CostUSD: 1})
	_ = s.AppendUsage(UsageEvent{TS: now, SessionID: "new", Provider: "p", CostUSD: 1})
	if err := s.PruneUsage(now - 86400*30); err != nil {
		t.Fatal(err)
	}
	total, _ := s.UsageTotal(0)
	if total.Events != 1 {
		t.Errorf("prune should leave only the recent row, got %d", total.Events)
	}
}
