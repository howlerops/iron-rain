package hub

import (
	"sync"
	"time"
)

// Throttling failed authentication attempts.
//
// The control plane is reachable from every host on the network — the --addr flag defaults to
// loopback but every shipped install overrides it to 0.0.0.0:6000 so the pairing QR can carry the
// LAN IP — and nothing bounded how fast a caller could guess. Credentials are 128 bits, so this was
// never a practical break; entropy was simply the only thing standing there, and "the secret is long"
// is a weak place for the sole defence to live. A weaker credential introduced later, by any path,
// would have had nothing behind it.
//
// DELAY ON FAILURE rather than lockout, deliberately. Locking after N failures hands any host on the
// network a way to deny the owner access to their own machine, which trades a theoretical attack for
// a practical one. A delay costs an attacker their guess rate and costs a legitimate client nothing:
// a correct credential is never delayed, because the wait is applied only on the failure path.
//
// The counter is GLOBAL, not per-key. Per-key would be trivially evaded — the client generates its
// own keypair, so an attacker rotates identity between guesses for free. What is worth bounding is
// the total rate of wrong answers reaching the daemon, whoever is offering them.

const (
	// authFailureWindow is how long a failure keeps counting toward the delay.
	authFailureWindow = 1 * time.Minute
	// authFailureGrace is how many failures are free. Fat-fingering a pairing code, an app retrying
	// with a stale credential after a re-pair — these are ordinary and should not feel punished.
	authFailureGrace = 3
	// authFailureStep is added per failure past the grace.
	authFailureStep = 250 * time.Millisecond
	// authFailureMax caps what ONE failure adds to the queue. At this step a sustained attacker is
	// held to ~1 wrong answer per second in total, rather than one per second per connection.
	authFailureMax = 1 * time.Second
	// authQueueMax caps how long any single attempt is made to wait, however deep the queue is. A
	// connection goroutine parked indefinitely is its own denial of service — the daemon holds one
	// per socket — so the queue bounds the RATE while this bounds the resource.
	authQueueMax = 5 * time.Second
)

// authThrottle counts recent authentication failures and converts them into a delay.
type authThrottle struct {
	mu       sync.Mutex
	failures []time.Time
	// gate is when the next delayed attempt is allowed to finish waiting.
	//
	// Without it the delay bounded nothing. Each failure slept on its OWN handshake goroutine, so N
	// concurrent sockets served N penalties in parallel and the whole cost of guessing was the cost
	// of opening more connections — which is free, and which an attacker doing this would already be
	// doing. The documented "~1 guess/sec" was the rate for an attacker who politely used one
	// connection. Scheduling each delayed attempt to start where the previous one ends makes the
	// penalties queue instead of overlap, so the bound is on total wrong answers per second rather
	// than per socket.
	gate time.Time
}

// penalty records a failure and returns how long the caller should be made to wait.
func (a *authThrottle) penalty(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Drop anything that has aged out. Done on the way in so the slice cannot grow without bound
	// under a sustained flood — this is the only thing that trims it.
	cutoff := now.Add(-authFailureWindow)
	kept := a.failures[:0]
	for _, t := range a.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	a.failures = append(kept, now)

	over := len(a.failures) - authFailureGrace
	if over <= 0 {
		return 0
	}
	step := time.Duration(over) * authFailureStep
	if step > authFailureMax {
		step = authFailureMax
	}
	// Queue behind whatever is already waiting, rather than running alongside it.
	start := now
	if a.gate.After(start) {
		start = a.gate
	}
	a.gate = start.Add(step)
	// The queue may not build past what anyone will actually serve. Without this the gate only ever
	// moves forward: a sustained flood pushes it minutes into the future and it never comes back, so
	// long after the failures have aged out the OWNER's next mistyped code still waits the full cap.
	// Bounding it here keeps the penalty a function of the CURRENT failure rate.
	if limit := now.Add(authQueueMax); a.gate.After(limit) {
		a.gate = limit
	}
	wait := a.gate.Sub(now)
	// The honest residual, stated rather than hidden: a flood still gets a bounded wait per attempt,
	// because a connection goroutine parked for minutes is its own denial of service — the daemon has
	// one per socket. So a large enough burst is still served faster than the queue implies. What
	// this does buy is that the delay is now a real function of the global failure rate instead of a
	// per-socket sleep an attacker opts out of by opening another socket.
	if wait > authQueueMax {
		wait = authQueueMax
	}
	return wait
}

// recentFailures reports how many failures are inside the window, for tests and diagnostics.
func (a *authThrottle) recentFailures(now time.Time) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := now.Add(-authFailureWindow)
	n := 0
	for _, t := range a.failures {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}
