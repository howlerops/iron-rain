package opencode

import (
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
)

// TestEmitDoesNotRaceWithChannelClose drives the exact shape that failed in CI.
//
// `readLoop` closes `s.events` while `emit` — called from the goroutine that POSTs a turn — may
// still be sending on it. The old code guarded emit with `select { case s.events <- ev: case
// <-s.done: }`, which does not help: a send on a CLOSED channel is immediately ready, so select can
// pick it and panic. The failure is timing-dependent, which is why it passed locally for weeks and
// then took down a CI run.
//
// Run with -race. Without the emitMu guard this fails as either a detected data race or an outright
// `panic: send on closed channel`; the loop and the deliberately small buffer exist to make the
// window wide enough to hit reliably rather than once in a hundred runs.
func TestEmitDoesNotRaceWithChannelClose(t *testing.T) {
	for range 200 {
		s := &session{
			// Small buffer: emitters park in the select, which is precisely where the old code was
			// vulnerable to a concurrent close.
			events: make(chan agent.Event, 1),
			done:   make(chan struct{}),
		}

		// A consumer that stops early, so the buffer fills and senders block — the hub does the same
		// thing when a client disconnects mid-turn.
		go func() {
			for range s.events {
				return
			}
		}()

		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 20 {
					s.emit(agent.Event{})
				}
			}()
		}

		// Close concurrently with the emitters, mirroring readLoop's deferred close.
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Microsecond)
			_ = s.Close() // releases anyone parked in emit's select
			s.emitMu.Lock()
			s.eventsClosed = true
			close(s.events)
			s.emitMu.Unlock()
		}()

		wg.Wait()
	}
}

// TestEmitAfterCloseIsDropped pins the post-close contract: emitting after the session has ended is
// a no-op, not a panic. Callers legitimately race here — a turn can still be finishing when the
// stream drops — so "drop it quietly" is the correct behaviour, and worth asserting so nobody
// later "fixes" the guard by removing it.
func TestEmitAfterCloseIsDropped(t *testing.T) {
	s := &session{events: make(chan agent.Event, 1), done: make(chan struct{})}
	_ = s.Close()
	s.emitMu.Lock()
	s.eventsClosed = true
	close(s.events)
	s.emitMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.emit(agent.Event{}) // must return, not panic
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emit blocked after the session closed")
	}
}
