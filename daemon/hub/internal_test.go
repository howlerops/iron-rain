package hub

import (
	"bytes"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/transport"
)

// TestBroadcastDropsStalledSubscriber verifies the core fan-out fix: broadcast enqueues
// without blocking, and a subscriber whose bounded queue overflows (here, one with no
// writer draining it — a wedged client) is dropped rather than allowed to stall the
// single run() goroutine that pumps the provider event stream.
func TestBroadcastDropsStalledSubscriber(t *testing.T) {
	m := newManagedSession(nil, nil, sessionMeta{})
	// A subscriber whose queue is never drained. Registered directly so no writer
	// goroutine exists to consume it — modelling a client whose socket has wedged.
	s := &subscriber{conn: &transport.Conn{}, ch: make(chan []byte, 4), done: make(chan struct{})}
	m.mu.Lock()
	m.subs[s.conn] = s
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ { // far exceeds the queue depth
			m.broadcast([]byte("e"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a stalled subscriber (head-of-line blocking)")
	}

	m.mu.Lock()
	_, still := m.subs[s.conn]
	m.mu.Unlock()
	if still {
		t.Fatal("stalled subscriber was not dropped from the fan-out set")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("dropped subscriber's writer was not signalled to stop")
	}
}

// TestTranscriptCapped verifies the session transcript is a bounded ring buffer: a
// long-lived session appending events forever must not grow memory without limit.
func TestTranscriptCapped(t *testing.T) {
	m := newManagedSession(nil, nil, sessionMeta{})
	ev := bytes.Repeat([]byte("x"), 64)
	for i := 0; i < maxTranscriptEvents*4; i++ {
		m.broadcast(ev)
	}
	m.mu.Lock()
	n, size := len(m.transcript), m.transcriptBytes
	m.mu.Unlock()
	if n > maxTranscriptEvents {
		t.Fatalf("transcript not capped by count: got %d, cap %d", n, maxTranscriptEvents)
	}
	if size > maxTranscriptBytes {
		t.Fatalf("transcript not capped by bytes: got %d, cap %d", size, maxTranscriptBytes)
	}
	// transcriptBytes must stay consistent with the retained slice.
	want := 0
	m.mu.Lock()
	for _, e := range m.transcript {
		want += len(e)
	}
	got := m.transcriptBytes
	m.mu.Unlock()
	if got != want {
		t.Fatalf("transcriptBytes drifted: field=%d actual=%d", got, want)
	}
}

// TestTranscriptByteCap verifies the byte cap trims even when the event count is low but
// individual events are large.
func TestTranscriptByteCap(t *testing.T) {
	m := newManagedSession(nil, nil, sessionMeta{})
	big := bytes.Repeat([]byte("y"), 1<<20) // 1 MiB each
	for i := 0; i < 32; i++ {                // 32 MiB total pushed, cap is 8 MiB
		m.broadcast(big)
	}
	m.mu.Lock()
	size := m.transcriptBytes
	m.mu.Unlock()
	if size > maxTranscriptBytes {
		t.Fatalf("byte cap not enforced: got %d, cap %d", size, maxTranscriptBytes)
	}
}

// TestReservePortReleased verifies reserved worktree ports are returned to the pool, so
// the reservedPorts set doesn't grow forever and exhaust the range.
func TestReservePortReleased(t *testing.T) {
	h := New()
	p := h.reservePort(38000, 38050)
	if p == 0 {
		t.Skip("no free port in range to test with")
	}
	h.mu.Lock()
	reserved := h.reservedPorts[p]
	h.mu.Unlock()
	if !reserved {
		t.Fatalf("port %d not marked reserved after reservePort", p)
	}
	h.releasePort(p)
	h.mu.Lock()
	stillReserved := h.reservedPorts[p]
	h.mu.Unlock()
	if stillReserved {
		t.Fatalf("port %d still reserved after releasePort", p)
	}
}
