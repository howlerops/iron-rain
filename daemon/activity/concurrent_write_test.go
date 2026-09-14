package activity

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The activity log's two durable writers must be serialised against each other.
//
// appendLine and rewrite both run AFTER s.mu is released — correctly, so disk I/O never blocks the
// in-memory ring — but nothing serialised them against one another, and they are not independent:
//
//   - rewrite os.Creates a FIXED ".tmp" path and renames it over the log, so two concurrent rewrites
//     interleave into a single temp file and whichever renames last publishes the mess.
//   - appendLine opens the log and then writes. If a rename lands between those two steps, the write
//     goes to an inode that has just been unlinked, and the event is gone.
//
// This is asserted STRUCTURALLY rather than by hammering it. The window between open and write is a
// few instructions wide; a green run of a concurrent stress test says nothing about whether the lock
// is there, which is the same trap the deadlock test next door fell into. What matters is the
// invariant — every durable write holds fileMu — and that is checkable exactly.
func TestEveryDurableWriteIsSerialised(t *testing.T) {
	src, err := os.ReadFile("activity.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// The two functions that touch the file, and the guard each must take.
	for _, fn := range []string{"func (s *Store) appendLine(", "func (s *Store) rewrite("} {
		i := strings.Index(body, fn)
		if i < 0 {
			t.Fatalf("%s is gone — this test can no longer tell whether the log is written safely", fn)
		}
		end := strings.Index(body[i:], "\n}\n")
		if end < 0 {
			t.Fatalf("%s is not terminated", fn)
		}
		fnBody := body[i : i+end]
		if !strings.Contains(fnBody, "s.fileMu.Lock()") {
			t.Errorf("%s writes to disk without taking fileMu.\n\nA rename landing between its open "+
				"and its write sends the event to an unlinked inode, and two rewrites racing "+
				"interleave into one temp file. Neither is visible until a restart: the event is in "+
				"the ring so every connected client saw it, and it is simply not in the file — the "+
				"Activity feed and the Needs-You inbox come back missing items.", fn)
		}
		if strings.Contains(fnBody, "s.mu.Lock()") {
			t.Errorf("%s takes the RING lock. Disk I/O must not block the in-memory ring; that is "+
				"why fileMu is a separate mutex.", fn)
		}
	}
}

// And the behaviour that guard exists to protect: everything the ring holds is on disk, so a restart
// loses nothing. Fast, and it exercises both durable paths.
func TestTheRingSurvivesAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	s := New(path, 500)
	if s == nil {
		t.Fatal("store did not open")
	}

	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if w%2 == 0 {
					s.Record(Event{Kind: KindFinished, SessionID: "s", Title: "done"}) // append path
				} else {
					s.Record(Event{Kind: KindStalled, SessionID: "n", Title: "stuck", NeedsYou: true})
					s.MarkRead([]string{"n"}) // rewrite path
				}
			}
		}(w)
	}
	wg.Wait()

	want := len(s.Recent())
	if want == 0 {
		t.Fatal("nothing was recorded")
	}
	if got := len(New(path, 500).Recent()); got != want {
		t.Errorf("the log came back with %d of %d events after a reload", got, want)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("a .tmp file survived — a rewrite was interrupted partway through")
	}
}
