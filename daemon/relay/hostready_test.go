package relay

import (
	"testing"
	"time"
)

// waitForHost blocks until the relay has actually registered a host for id.
//
// Dialling only completes the HTTP upgrade. serveHost claims the slot on its own goroutine
// afterwards, so a client dialled immediately after a host can legitimately arrive first and be
// refused "no host for server_id". That is CORRECT relay behaviour — the app races LAN and several
// relays and wants a fast no rather than a hang, and the Cloudflare relay refuses identically — but
// in a test it is a race against ourselves.
//
// It is what made TestASlowClientLosesNoFrames flaky: one failure in a full parallel `go test ./...`
// run, never reproducible in isolation. Sleeping for a guessed interval is the same race with a
// bigger constant; this waits for the condition.
func waitForHost(t *testing.T, r *Relay, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		_, ok := r.hosts[id]
		r.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the relay never registered a host for %q", id)
}
