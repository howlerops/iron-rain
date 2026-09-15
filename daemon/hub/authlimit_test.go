package hub

import (
	"testing"
	"time"
)

// The first few failures are free: a mistyped pairing code and an app retrying with a credential
// that was rotated out are both ordinary, and neither should feel punished.
func TestAuthThrottleGraceIsFree(t *testing.T) {
	var a authThrottle
	now := time.Now()
	for i := 0; i < authFailureGrace; i++ {
		if d := a.penalty(now); d != 0 {
			t.Fatalf("failure %d inside the grace incurred %v, want 0", i+1, d)
		}
	}
}

// Past the grace the wait grows, and then stops growing — an unbounded delay would let a flood pin
// goroutines indefinitely, which trades one denial-of-service for another.
//
// The cap is authQueueMax, not authFailureMax: penalties now QUEUE (see the test below), so
// authFailureMax bounds what one failure adds to the queue while this bounds what any single attempt
// is made to wait. Before the queue existed both were the same number, and this test asserted it.
func TestAuthThrottleGrowsThenCaps(t *testing.T) {
	var a authThrottle
	now := time.Now()
	var last time.Duration
	for i := 0; i < authFailureGrace+40; i++ {
		last = a.penalty(now)
	}
	if last != authQueueMax {
		t.Fatalf("sustained failures settled at %v, want the cap %v", last, authQueueMax)
	}

	var b authThrottle
	for i := 0; i < authFailureGrace; i++ {
		b.penalty(now)
	}
	first := b.penalty(now)
	second := b.penalty(now)
	if !(first > 0 && second > first) {
		t.Fatalf("penalty should increase with consecutive failures: %v then %v", first, second)
	}
}

// A machine that failed a few times an hour ago is not under attack. Without ageing, one bad week
// would leave the owner permanently throttled.
func TestAuthThrottleForgetsOldFailures(t *testing.T) {
	var a authThrottle
	start := time.Now()
	for i := 0; i < authFailureGrace+10; i++ {
		a.penalty(start)
	}
	later := start.Add(authFailureWindow + time.Second)
	if n := a.recentFailures(later); n != 0 {
		t.Fatalf("%d failures still counted after the window elapsed, want 0", n)
	}
	if d := a.penalty(later); d != 0 {
		t.Fatalf("a failure after the window should be back inside the grace, got %v", d)
	}
}

// The failure log must not grow without bound under a sustained flood — it is trimmed on the way in,
// and nothing else trims it.
func TestAuthThrottleDoesNotGrowUnbounded(t *testing.T) {
	var a authThrottle
	now := time.Now()
	for i := 0; i < 5000; i++ {
		// Spread across several windows so most should be swept.
		a.penalty(now.Add(time.Duration(i) * time.Second))
	}
	a.mu.Lock()
	held := len(a.failures)
	a.mu.Unlock()
	if held > 5000 {
		t.Fatalf("retained %d failures; the trim is not running", held)
	}
	// One per second against a one-minute window: roughly a window's worth should remain, not all of
	// them.
	if held > int(authFailureWindow/time.Second)+2 {
		t.Errorf("retained %d failures, expected about %d", held, int(authFailureWindow/time.Second))
	}
}

// Concurrency: the accept path runs per connection, so the throttle is hit from many goroutines.
func TestAuthThrottleIsConcurrencySafe(t *testing.T) {
	var a authThrottle
	now := time.Now()
	done := make(chan struct{})
	for i := 0; i < 32; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				a.penalty(now)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 32; i++ {
		<-done
	}
	if n := a.recentFailures(now); n != 32*50 {
		t.Fatalf("counted %d failures, want %d — a race lost some", n, 32*50)
	}
}

// Penalties must QUEUE, not run alongside each other.
//
// Each failure used to sleep on its own handshake goroutine, so N concurrent sockets served N
// penalties in parallel: the cost of guessing was the cost of opening another connection, which is
// free and which an attacker doing this is already doing. The documented "~1 guess/sec" was the rate
// for an attacker who politely used one connection at a time.
//
// Asserted without sleeping, by giving every attempt the SAME instant: if the delays overlap, they
// all come back the same; if they queue, each one starts where the last ended.
func TestAuthThrottlePenaltiesQueueRatherThanOverlap(t *testing.T) {
	var a authThrottle
	now := time.Now()
	for i := 0; i < authFailureGrace; i++ {
		a.penalty(now) // burn the grace
	}

	// Simultaneous failures, all at the same instant. A growing return value proves nothing on its
	// own — the per-failure STEP grows with the failure count whether or not anything queues — so the
	// test is whether a later attempt inherits the backlog ahead of it and therefore waits longer
	// than any single failure is allowed to add.
	var last time.Duration
	for i := 0; i < 12; i++ {
		last = a.penalty(now)
	}
	if last <= authFailureMax {
		t.Fatalf("the 12th simultaneous failure waited %v, no more than one failure's own cap (%v).\n\n"+
			"The penalties elapse in PARALLEL: each one slept on its own handshake goroutine, so N "+
			"concurrent sockets served N penalties at once and an attacker paid the cost of one guess "+
			"however many were in flight. Opening another connection is free, so the only rate control "+
			"in the auth path provided none.", last, authFailureMax)
	}

	// And the queue drains: an attempt well after the gate has passed waits only its own step.
	var b authThrottle
	for i := 0; i < authFailureGrace+3; i++ {
		b.penalty(now)
	}
	if d := b.penalty(now.Add(authFailureWindow + time.Second)); d != 0 {
		t.Errorf("a failure after the window still inherited a queued delay of %v — the gate has to "+
			"age out with the failures, or one burst throttles the owner for good", d)
	}
}

// The queue must not outlive the flood that built it.
//
// The gate only ever moved forward, so a sustained attack pushed it minutes into the future and
// nothing brought it back. Long after the failures had aged out of the window, the OWNER's next
// mistyped pairing code still waited the full cap — a penalty for someone else's traffic.
func TestTheThrottleQueueDoesNotOutliveTheFlood(t *testing.T) {
	var a authThrottle
	now := time.Now()
	for i := 0; i < 500; i++ { // the flood
		a.penalty(now)
	}

	a.mu.Lock()
	gate := a.gate
	a.mu.Unlock()
	if gate.After(now.Add(authQueueMax)) {
		t.Fatalf("the gate is %v into the future, past the %v anyone actually waits.\n\n"+
			"It never comes back, so every later failure — including the owner's own — inherits a "+
			"queue built by traffic that is long gone.", gate.Sub(now).Round(time.Second), authQueueMax)
	}

	// And once the window has emptied, a single failure is back inside the grace.
	later := now.Add(authFailureWindow + time.Second)
	if d := a.penalty(later); d != 0 {
		t.Errorf("a lone failure after the window still waited %v", d)
	}
}
