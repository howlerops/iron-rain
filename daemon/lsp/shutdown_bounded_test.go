package lsp

import (
	"sync"
	"testing"
	"time"
)

// Teardown must finish even when a child never does.
//
// Shutdown used to end in a bare wg.Wait(). A language server that ignored its exit request held the
// daemon open forever: it had already stopped accepting connections, so it was unreachable, yet it
// stayed alive holding the state lock and every restart failed with "another oculusd is already
// using …". Observed at 90 minutes; only SIGKILL cleared it.
func TestWaitBoundedGivesUp(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { time.Sleep(30 * time.Second); wg.Done() }() // a child that will not exit

	start := time.Now()
	ok := waitBounded(&wg, 150*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("waitBounded claimed a stuck child had finished")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waitBounded blocked for %s — shutdown would hang and the lock would never be released", elapsed)
	}
}

// ...and it must still report success when the children DO exit, or shutdown would always look like
// it had leaked something.
func TestWaitBoundedReportsCleanExit(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() { time.Sleep(10 * time.Millisecond); wg.Done() }()
	}
	if !waitBounded(&wg, 3*time.Second) {
		t.Fatal("waitBounded reported a leak for children that exited cleanly")
	}
}
