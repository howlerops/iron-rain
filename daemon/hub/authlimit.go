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
	// authFailureMax caps the wait, so a sustained flood cannot pin goroutines for long. At this cap
	// a single attacker gets ~1 guess/sec against a 128-bit space.
	authFailureMax = 1 * time.Second
)

// authThrottle counts recent authentication failures and converts them into a delay.
type authThrottle struct {
	mu       sync.Mutex
	failures []time.Time
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
	d := time.Duration(over) * authFailureStep
	if d > authFailureMax {
		d = authFailureMax
	}
	return d
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
