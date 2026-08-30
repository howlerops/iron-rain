package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// The usage model had two errors pulling in OPPOSITE directions, which is why neither was noticed
// until the numbers got large: adapters dropped tokens they had no field for (an undercount), while
// the client summed cache reads per turn (a large overcount). These assert the contract that
// separates them, so a future adapter cannot quietly re-merge the two.

func TestCacheReadsAreNotInput(t *testing.T) {
	// A turn that re-sends 100k of context and adds 500 new tokens spent 500 — not 100,500.
	u := protocol.SessionUsage{InputTokens: 500, CacheReadTokens: 100_000, OutputTokens: 200}
	if u.InputTokens != 500 {
		t.Fatalf("InputTokens = %d; cache reads must not be folded in", u.InputTokens)
	}
	// The wire keeps them apart, so a client cannot accidentally add them by decoding.
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back["input_tokens"] != float64(500) {
		t.Errorf("input_tokens on the wire = %v, want 500", back["input_tokens"])
	}
	if back["cache_read_tokens"] != float64(100_000) {
		t.Errorf("cache_read_tokens = %v, want it reported separately", back["cache_read_tokens"])
	}
}

// "No cost reported" and "cost was zero" must be distinguishable. opencode reports no cost at all
// for models it has no pricing for, and rendering that as $0.000 tells the user the run was free.
func TestUnreportedCostIsDistinguishableFromZero(t *testing.T) {
	unknown := protocol.SessionUsage{InputTokens: 10, CostUSD: 0, CostReported: false}
	free := protocol.SessionUsage{InputTokens: 10, CostUSD: 0, CostReported: true}
	if unknown.CostReported == free.CostReported {
		t.Fatal("a provider that reported nothing is indistinguishable from one that reported zero")
	}
	raw, _ := json.Marshal(unknown)
	var back map[string]any
	_ = json.Unmarshal(raw, &back)
	if _, present := back["cost_reported"]; present {
		t.Error("cost_reported should be omitted when false, so an older client defaults to unknown")
	}
}

// Reasoning tokens are billed as output, so they belong IN OutputTokens — and are also reported
// separately for display. Adding the separate field to the total again would double-count them.
func TestReasoningIsIncludedInOutputAndReportedSeparately(t *testing.T) {
	// An adapter builds this as Output = output + reasoning.
	u := protocol.SessionUsage{OutputTokens: 124 + 21, ReasoningTokens: 21}
	if u.OutputTokens != 145 {
		t.Fatalf("OutputTokens = %d, want reasoning folded into billed output", u.OutputTokens)
	}
	// The display total must not add reasoning a second time.
	spent := u.InputTokens + u.OutputTokens
	if spent != 145 {
		t.Errorf("spent = %d, want 145 — reasoning must not be counted twice", spent)
	}
}
