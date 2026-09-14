package main

import (
	"os"
	"strings"
	"testing"
)

// The log panel's seeding must happen BEFORE the log is rolled.
//
// SetOutput tees the standard logger into the ring, and rollLogIfLarge ends by logging that it
// rolled. So on any daemon whose log had grown past the cap, the ring was already non-empty when
// SeedFromFile ran — and SeedFromFile bails on exactly that condition, because a non-empty ring means
// "this run has already logged, and seeding now would present its own output as history".
//
// The effect was that the previous run's tail was carried over on every start EXCEPT the ones where
// the log was large: long-running daemons, which is the population that crashes and the entire reason
// the feature exists. The panel could then only ever show the healthy replacement process.
//
// Asserted on source order because both statements are individually correct; it is the sequence that
// was wrong.
func TestTheLogRingIsSeededBeforeTheLogIsRolled(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	seed := strings.Index(body, "lh.SeedFromFile(")
	if seed < 0 {
		t.Fatal("SeedFromFile is gone — the log panel can no longer show the run that broke")
	}
	roll := strings.Index(body, "rollLogIfLarge(")
	if roll < 0 {
		t.Fatal("rollLogIfLarge is gone — the on-disk log is unbounded")
	}
	if seed > roll {
		t.Errorf("SeedFromFile (offset %d) runs AFTER rollLogIfLarge (offset %d).\n\n"+
			"rollLogIfLarge logs on the roll path, that line lands in the ring, and SeedFromFile "+
			"returns early on a non-empty ring — so the previous run's tail is dropped on exactly the "+
			"daemons whose logs are large enough to roll.", seed, roll)
	}
}
