package hub

import (
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
)

// TestHistoryPageWalksBackwards: paging is by COUNT ALREADY LOADED, so the daemon keeps no
// per-client cursor and two devices on the same session can page independently.
func TestHistoryPageWalksBackwards(t *testing.T) {
	h := New()
	m := newManagedSession(h, &approvalFakeSess{ch: make(chan agent.Event, 1)}, sessionMeta{})

	// 500 events, oldest first.
	m.mu.Lock()
	for i := 0; i < 500; i++ {
		m.transcript = append(m.transcript, []byte{byte(i / 256), byte(i % 256)})
	}
	m.mu.Unlock()

	// First page back from a tail of 200.
	page, more := m.historyPage(200, 100)
	if len(page) != 100 {
		t.Fatalf("first page = %d events, want 100", len(page))
	}
	if !more {
		t.Error("200 more remain, so hasMore must be true")
	}
	// It must be the events immediately BEFORE the loaded tail — [200,300) of 500.
	if idxOf(page[0]) != 200 || idxOf(page[99]) != 299 {
		t.Errorf("first page covered the wrong range: %d..%d", idxOf(page[0]), idxOf(page[99]))
	}

	// Walking further back.
	page, more = m.historyPage(300, 100)
	if idxOf(page[0]) != 100 || idxOf(page[99]) != 199 {
		t.Errorf("second page covered the wrong range: %d..%d", idxOf(page[0]), idxOf(page[99]))
	}
	if !more {
		t.Error("100 still remain")
	}

	// The final page reports no more.
	page, more = m.historyPage(400, 100)
	if len(page) != 100 || more {
		t.Errorf("final page = %d events, more=%v; want 100 and false", len(page), more)
	}

	// Past the beginning is empty, not negative or panicking.
	page, more = m.historyPage(500, 100)
	if len(page) != 0 || more {
		t.Errorf("paging past the start must be empty, got %d/%v", len(page), more)
	}
	page, _ = m.historyPage(9999, 100)
	if len(page) != 0 {
		t.Errorf("an over-large loaded count must be empty, got %d", len(page))
	}
}

// TestHistoryPageClampsToStart: a limit larger than what remains returns what's there.
func TestHistoryPageClampsToStart(t *testing.T) {
	h := New()
	m := newManagedSession(h, &approvalFakeSess{ch: make(chan agent.Event, 1)}, sessionMeta{})
	m.mu.Lock()
	for i := 0; i < 30; i++ {
		m.transcript = append(m.transcript, []byte{byte(i)})
	}
	m.mu.Unlock()

	page, more := m.historyPage(10, 100)
	if len(page) != 20 {
		t.Fatalf("expected the remaining 20 events, got %d", len(page))
	}
	if more {
		t.Error("nothing older remains")
	}
	if page[0][0] != 0 {
		t.Errorf("a clamped page must start at the very beginning, got %v", page[0])
	}
}

// idxOf decodes the two-byte index the fixture writes into each event.
func idxOf(raw []byte) int { return int(raw[0])*256 + int(raw[1]) }

// TestReplayTailLimitIsSane guards the constant the whole design leans on.
func TestReplayTailLimitIsSane(t *testing.T) {
	if replayTailLimit < 50 {
		t.Errorf("tail of %d is too small — most conversations should arrive whole", replayTailLimit)
	}
	if replayTailLimit > 1000 {
		t.Errorf("tail of %d defeats the point of paging", replayTailLimit)
	}
}
