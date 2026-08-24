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
func TestAuthThrottleGrowsThenCaps(t *testing.T) {
	var a authThrottle
	now := time.Now()
	var last time.Duration
	for i := 0; i < authFailureGrace+40; i++ {
		last = a.penalty(now)
	}
	if last != authFailureMax {
		t.Fatalf("sustained failures settled at %v, want the cap %v", last, authFailureMax)
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
