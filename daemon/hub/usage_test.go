package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/store"
)

func usageHub(t *testing.T) *Hub {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Hub{db: db, sessions: map[string]*managedSession{}}
}

// TestUsageReportSpansSessions is the point of persisting usage: the report covers work whose
// session is long gone, which the live in-memory meter never could.
func TestUsageReportSpansSessions(t *testing.T) {
	h := usageHub(t)
	now := time.Now().Unix()
	for _, e := range []store.UsageEvent{
		{TS: now - 60, SessionID: "gone", Provider: "opencode", Model: "sonnet", InTokens: 1000, OutTokens: 200, CostUSD: 0.30},
		{TS: now - 30, SessionID: "live", Provider: "claude-code", Model: "opus", InTokens: 500, OutTokens: 100, CostUSD: 1.20},
	} {
		if err := h.db.AppendUsage(e); err != nil {
			t.Fatal(err)
		}
	}
	r := h.usageReport()
	if r.Today.CostUSD < 1.49 || r.Today.CostUSD > 1.51 {
		t.Errorf("today = %f, want ~1.50", r.Today.CostUSD)
	}
	if r.Today.InputTokens != 1500 {
		t.Errorf("today input tokens = %d, want 1500", r.Today.InputTokens)
	}
	if len(r.Providers) != 2 {
		t.Errorf("want both providers, got %d", len(r.Providers))
	}
	// Models are ordered by spend, so the expensive one leads.
	if len(r.Models) != 2 || r.Models[0].Key != "opus" {
		t.Errorf("models = %+v, want opus first", r.Models)
	}
	// No live session claims a subscription provider, so costs are presented as real.
	if r.Subscription {
		t.Error("no live sessions — subscription must not be asserted")
	}
}

// TestUsageWindowAnchorsToFirstActivity: the window resets 5h after usage BEGAN, not on a clock
// boundary — a countdown derived from the wrong anchor would be wrong all day.
func TestUsageWindowAnchorsToFirstActivity(t *testing.T) {
	h := usageHub(t)
	now := time.Now()
	start := now.Add(-2 * time.Hour).Unix()
	_ = h.db.AppendUsage(store.UsageEvent{TS: start, SessionID: "a", Provider: "p", CostUSD: 0.5, InTokens: 10})
	_ = h.db.AppendUsage(store.UsageEvent{TS: now.Add(-time.Minute).Unix(), SessionID: "a", Provider: "p", CostUSD: 0.25, InTokens: 5})

	w := h.usageWindow(now)
	if !w.Active {
		t.Fatal("usage 2h ago is inside a 5h window — it must be active")
	}
	if w.StartedAt != start {
		t.Errorf("window start = %d, want the first event (%d)", w.StartedAt, start)
	}
	if w.ResetsAt != start+5*3600 {
		t.Errorf("reset = %d, want start+5h (%d)", w.ResetsAt, start+5*3600)
	}
	if w.CostUSD < 0.74 || w.CostUSD > 0.76 {
		t.Errorf("window cost = %f, want ~0.75 (everything since the anchor)", w.CostUSD)
	}
}

// TestUsageWindowIdleWhenStale: usage older than the window consumes no current allowance, so the
// UI must not show a stale countdown.
func TestUsageWindowIdleWhenStale(t *testing.T) {
	h := usageHub(t)
	now := time.Now()
	_ = h.db.AppendUsage(store.UsageEvent{TS: now.Add(-9 * time.Hour).Unix(), SessionID: "a", Provider: "p", CostUSD: 5})
	w := h.usageWindow(now)
	if w.Active || w.ResetsAt != 0 {
		t.Errorf("9h-old usage must leave the window idle, got %+v", w)
	}
	if w.Hours != usageWindowHours {
		t.Errorf("window length should still be reported, got %d", w.Hours)
	}
}

// TestUsageReportWithoutStore: a daemon running without a database must return an empty report
// rather than panic — the slices are non-nil so clients don't have to special-case null.
func TestUsageReportWithoutStore(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}
	r := h.usageReport()
	if r.Providers == nil || r.Models == nil || r.Sessions == nil {
		t.Error("breakdowns must be empty arrays, not null")
	}
	if r.Window.Active {
		t.Error("no store means no window")
	}
}
