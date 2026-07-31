package hub

import (
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
)

// TestTrimMarksTheRingAsAWindow: once the ring drops an event it is no longer the whole session, and
// replaying it as if it were is what made a long conversation look like it had lost its history.
func TestTrimMarksTheRingAsAWindow(t *testing.T) {
	h := New()
	m := newManagedSession(h, &approvalFakeSess{ch: make(chan agent.Event, 1)}, sessionMeta{})

	m.mu.Lock()
	if m.transcriptTrimmed {
		t.Fatal("a fresh session's ring is complete, not a window")
	}
	// Under the caps: nothing dropped, still complete.
	for i := 0; i < 10; i++ {
		m.transcript = append(m.transcript, []byte("event"))
		m.transcriptBytes += 5
	}
	m.trimTranscript()
	if m.transcriptTrimmed {
		t.Fatal("trimming below the caps must not mark the ring as trimmed")
	}
	m.mu.Unlock()

	// Blow the BYTE cap — the case that actually bit: a few huge tool outputs, not many small ones.
	m.mu.Lock()
	big := make([]byte, maxTranscriptBytes/2)
	for i := 0; i < 3; i++ {
		m.transcript = append(m.transcript, big)
		m.transcriptBytes += len(big)
	}
	m.trimTranscript()
	trimmed := m.transcriptTrimmed
	remaining := len(m.transcript)
	m.mu.Unlock()

	if !trimmed {
		t.Fatal("dropping events to satisfy the byte cap must mark the ring as a window")
	}
	if remaining == 0 {
		t.Fatal("trimming must keep the recent window, not empty the ring")
	}
	// The point of the flag: a trimmed ring is small even though the session was long.
	if remaining > 3 {
		t.Errorf("expected the byte cap to drop most events, %d remain", remaining)
	}
}

// TestTranscriptCapsAreSane guards the constants the above depends on.
func TestTranscriptCapsAreSane(t *testing.T) {
	if maxTranscriptBytes <= 0 || maxTranscriptEvents <= 0 {
		t.Fatal("caps must be positive")
	}
	// The durable store keeps more finalized events than the ring keeps raw ones — that asymmetry is
	// exactly why the durable transcript is worth replaying in front of a trimmed ring.
	if maxTranscriptEvents > 5000 {
		t.Errorf("ring event cap (%d) should stay under the durable row cap", maxTranscriptEvents)
	}
}
