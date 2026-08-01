package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestHeartbeatSnapshotOnConnect: heartbeatTick only broadcasts on CHANGE, so a device that connects
// while a session is already working heard nothing at all until the state next flipped — up to a
// full 25s tick of a blank chip on the exact swap the product is built around. A connecting client
// must be handed the current state immediately.
func TestHeartbeatSnapshotOnConnect(t *testing.T) {
	h := New()
	fake := &turnFakeSess{ch: nil}
	m := newManagedSession(h, fake, sessionMeta{})
	m.lastActivity = time.Now() // actively working
	h.mu.Lock()
	h.sessions[fake.ID()] = m
	frames := make(chan []byte, 32)
	h.clients[nil] = &hubClient{ch: frames, done: make(chan struct{})}
	h.mu.Unlock()

	h.SendHeartbeatSnapshot(nil)

	select {
	case raw := <-frames:
		env, err := protocol.Decode(raw)
		if err != nil || env.Type != protocol.TypeSessionHeartbeat {
			t.Fatalf("first frame is %v (%v), want %s", env.Type, err, protocol.TypeSessionHeartbeat)
		}
		var hb protocol.SessionHeartbeat
		if json.Unmarshal(env.Payload, &hb) != nil || hb.SessionID != fake.ID() || hb.State != hbWorking {
			t.Fatalf("snapshot = %+v, want session %s in state %s", hb, fake.ID(), hbWorking)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat snapshot for a connecting client — the chip stays blank until the next tick")
	}
}
