package loghub

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The log panel exists to answer "what happened when it broke", and it structurally could not.
//
// The ring is in-memory and starts empty on every launch, so the lines from the run that died were
// never in it — by the time anyone opened the panel they were reading the healthy process that had
// just replaced the broken one. The evidence was on disk the entire time; nothing read it.
func TestTheRingIsSeededWithThePreviousRunsTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oculusd.log")
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	b.WriteString("panic: the thing that actually broke\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(1000)
	h.SeedFromFile(path, 300)
	got := h.Recent()

	if len(got) == 0 {
		t.Fatal("the ring is empty — the panel still cannot show the run that broke")
	}
	if got[len(got)-1] != RestartMarker {
		t.Errorf("last seeded line = %q, want the restart marker — without it the previous run's "+
			"output reads as if it is happening now", got[len(got)-1])
	}
	if got[len(got)-2] != "panic: the thing that actually broke" {
		t.Errorf("the last line before the marker is %q, want the panic — the tail is the part that "+
			"matters and it is what got dropped", got[len(got)-2])
	}
	if len(got) != 301 {
		t.Errorf("seeded %d lines, want 300 + the marker — the tail is unbounded or truncated", len(got))
	}
	if strings.Contains(got[0], "line 0") {
		t.Error("seeded from the START of the file; a bounded TAIL is the point")
	}
}

// A roll and a restart coincide when the daemon has been running long enough to fill the log. The
// live file is then empty and everything worth reading is in the copy set aside beside it.
func TestSeedFallsBackToTheRolledAsideLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oculusd.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("the last thing before the roll\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(1000)
	h.SeedFromFile(path, 300)
	got := h.Recent()
	if len(got) != 2 || got[0] != "the last thing before the roll" {
		t.Errorf("recovered %v, want the rolled-aside tail — a restart that coincides with a roll "+
			"loses the history entirely", got)
	}
}

// Seeding must never relabel THIS run's output as history, and must not fail a start when there is
// no log to read.
func TestSeedingIsSafeWhenThereIsNothingToSeed(t *testing.T) {
	h := New(1000)
	h.SeedFromFile(filepath.Join(t.TempDir(), "does-not-exist.log"), 300)
	if got := h.Recent(); len(got) != 0 {
		t.Errorf("seeded %v from a missing file", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "oculusd.log")
	if err := os.WriteFile(path, []byte("older run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h2 := New(1000)
	h2.Write([]byte("this run started\n"))
	h2.SeedFromFile(path, 300)
	got := h2.Recent()
	if len(got) != 1 || got[0] != "this run started" {
		t.Errorf("ring = %v — seeding after the process has logged puts stale lines AFTER live ones, "+
			"which reads as the daemon travelling backwards in time", got)
	}
}
