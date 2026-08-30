package hub

import (
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Derived turn state must not enter the REPLAYABLE transcript.
//
// Two publishers announce a turn's end: the pump forwards the provider's own status (conversation —
// belongs in the ring) and the turn engine publishes derived state (ambient — does not). Routing the
// derived one through broadcast put a second terminal status in every turn's replay, so each future
// attach would render it as an extra row of history rather than the momentary repeat a live client
// harmlessly dedupes.
//
// Exercised through publishSessionState rather than through broadcastTransient directly: the
// primitive always behaved correctly, so a test calling it proves nothing about the call site — the
// first version of this test passed on the unfixed tree for exactly that reason.
func TestDerivedTurnStateStaysOutOfTheReplayRing(t *testing.T) {
	fake := &turnFakeSess{ch: make(chan agent.Event, 8)}
	m := newManagedSession(New(), fake, sessionMeta{})
	frames := make(chan []byte, 16)
	m.mu.Lock()
	m.subs[subscriberConnID] = &subscriber{conn: subscriberConnID, ch: frames, done: make(chan struct{})}
	m.mu.Unlock()

	m.publishSessionState(protocol.StatusIdle, "")

	// The subscriber still receives it — ordering with the turn's content is the whole point.
	select {
	case <-frames:
	default:
		t.Fatal("the subscriber received no derived status at all")
	}
	m.mu.Lock()
	ring := len(m.transcript)
	m.mu.Unlock()
	if ring != 0 {
		t.Errorf("derived turn state left %d frame(s) in the replay ring; every future attach would "+
			"replay them as history", ring)
	}
}
