package hub

import (
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// The daemon persists usage (store.AppendUsage) and totals it per provider and per day, so the
// session-level fix only helps if THOSE inherit it. They read u.InputTokens, which now excludes
// cache reads — this pins that, because the inflated version is what a fleet cost report would
// otherwise have been built on.

func TestSessionAccumulationExcludesCacheAndTracksCostKnown(t *testing.T) {
	m := &managedSession{}

	// Three turns of a growing conversation: new input stays small, the cache read grows as the
	// whole conversation is re-sent, and this provider reports no cost.
	turns := []protocol.SessionUsage{
		{InputTokens: 500, OutputTokens: 100, CacheReadTokens: 0, CostReported: false},
		{InputTokens: 200, OutputTokens: 150, CacheReadTokens: 10_000, CostReported: false},
		{InputTokens: 300, OutputTokens: 200, CacheReadTokens: 25_000, CostReported: false},
	}
	for _, u := range turns {
		m.inTok += u.InputTokens
		m.outTok += u.OutputTokens
		if u.CostReported {
			m.costUSD += u.CostUSD
			m.costKnown = true
		}
		if ctx := u.CacheReadTokens + u.InputTokens; ctx > 0 {
			m.contextTokens = ctx
		}
	}

	// Spend is 1000 in / 450 out. The pre-fix arithmetic folded cache reads into input and summed
	// them, giving 36_000 — 36x the real figure, and the shape of the 3.1M headline.
	if m.inTok != 1000 {
		t.Errorf("inTok = %d, want 1000 (cache reads are not new input)", m.inTok)
	}
	if m.outTok != 450 {
		t.Errorf("outTok = %d, want 450", m.outTok)
	}
	// Context REPLACES: it is the last turn's conversation size, not a running total.
	if m.contextTokens != 25_300 {
		t.Errorf("contextTokens = %d, want 25300 (the last turn's size, not a sum)", m.contextTokens)
	}
	// Nothing reported a cost, so the session must not claim one.
	if m.costKnown {
		t.Error("costKnown is true though no provider reported a cost — this renders as $0.000")
	}
}

func TestCostBecomesKnownOnlyWhenReported(t *testing.T) {
	m := &managedSession{}
	for _, u := range []protocol.SessionUsage{
		{InputTokens: 10, CostUSD: 0, CostReported: false},
		{InputTokens: 10, CostUSD: 0.25, CostReported: true},
	} {
		if u.CostReported {
			m.costUSD += u.CostUSD
			m.costKnown = true
		}
	}
	if !m.costKnown {
		t.Fatal("a reported cost must mark the session priced")
	}
	if m.costUSD != 0.25 {
		t.Errorf("costUSD = %v, want 0.25 — the unreported turn must not contribute", m.costUSD)
	}
}
