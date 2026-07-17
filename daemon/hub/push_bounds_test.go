package hub

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
)

// blockingNotifier signals on entered when Notify starts, then blocks until release is
// closed (or the ctx is cancelled) — so a test can observe how many Notify calls run
// concurrently.
type blockingNotifier struct {
	entered chan<- struct{}
	release chan struct{}
}

func (b *blockingNotifier) Notify(ctx context.Context, _ string, _ push.Notification) error {
	b.entered <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

// TestPushApprovalBoundsConcurrency proves the approval push fan-out never runs more than
// pushConcurrency Notify calls at once (so a burst of approvals to many devices can't
// spawn goroutines without limit), and that every push still completes.
func TestPushApprovalBoundsConcurrency(t *testing.T) {
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	fn := &blockingNotifier{entered: entered, release: release}

	h := New()
	h.pushConcurrency, h.pushTimeout = 2, 2*time.Second // per-instance; no shared global
	h.notifier = fn
	h.pushTokens = []string{"a", "b", "c", "d", "e", "f"}

	h.pushApproval(protocol.ApprovalRequest{ApprovalID: "ap1", Tool: "bash", SessionID: "s1"})

	// Exactly pushConcurrency notifies may start before any are released.
	for i := 0; i < h.pushConcurrency; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected %d concurrent notifies, only saw %d", h.pushConcurrency, i)
		}
	}
	// No further notify may start while the first batch is blocked on the semaphore.
	select {
	case <-entered:
		t.Fatalf("more than pushConcurrency (%d) notifies ran at once", h.pushConcurrency)
	case <-time.After(150 * time.Millisecond):
	}

	// Release the batch; all six pushes must ultimately complete.
	close(release)
	total := h.pushConcurrency
	for total < 6 {
		select {
		case <-entered:
			total++
		case <-time.After(2 * time.Second):
			t.Fatalf("not all notifies completed: %d/6", total)
		}
	}
}

// TestPushApprovalTimeoutDoesNotLeak proves a hung push provider doesn't wedge the fan-out:
// each Notify runs under pushTimeout, so even a Notify that ignores release returns via
// ctx cancellation and the whole fan-out drains.
func TestPushApprovalTimeoutDoesNotLeak(t *testing.T) {
	entered := make(chan struct{}, 16)
	fn := &blockingNotifier{entered: entered, release: make(chan struct{})} // never released

	h := New()
	h.pushConcurrency, h.pushTimeout = 4, 80*time.Millisecond
	h.notifier = fn
	h.pushTokens = []string{"a", "b", "c", "d"}

	h.pushApproval(protocol.ApprovalRequest{ApprovalID: "ap1", Tool: "bash", SessionID: "s1"})

	// All four Notify calls should start and then return via the pushTimeout deadline.
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("notify %d never ran", i)
		}
	}
}
