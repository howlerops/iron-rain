package hub

import (
	"errors"
	"testing"

	"github.com/howlerops/oculus/daemon/transport"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Relay health exists so the app can tell "remote access is down" from "the daemon is down".
//
// Before it, a daemon that could not register on its relay was silent from the phone's side: the app
// failed to reach it and said "daemon not running", sending someone to restart a process that was
// running perfectly. These assert the state the app renders and, just as importantly, how often it
// is told — a status display that emits a frame per retry costs more traffic than the sessions it
// reports on.

func TestRelayHealthReportsEachRelayInConfiguredOrder(t *testing.T) {
	h := New()
	h.SetRelayConnected("wss://a/ws")
	h.SetRelayFailed("wss://b/ws", errors.New("connect: connection refused"))

	got := h.RelayHealth().Relays
	if len(got) != 2 {
		t.Fatalf("got %d relays, want 2", len(got))
	}
	if got[0].URL != "wss://a/ws" || got[1].URL != "wss://b/ws" {
		t.Fatalf("order is %q, %q — the list must keep the order the operator configured, since "+
			"that order is their stated preference", got[0].URL, got[1].URL)
	}
	if !got[0].Connected || got[0].LastOKAt == 0 {
		t.Errorf("a connected relay reports %+v", got[0])
	}
	if got[1].Connected {
		t.Error("a refused relay reports itself connected")
	}
	if got[1].Detail == "" {
		t.Error("a failed relay carries no reason, so the app can only say that something is wrong")
	}
}

// "Down since boot" and "down for a moment" are different, and a boolean cannot tell them apart.
func TestARelayThatNeverCameUpIsDistinguishableFromOneThatDropped(t *testing.T) {
	h := New()
	h.SetRelayFailed("wss://never/ws", errors.New("no such host"))
	h.SetRelayConnected("wss://flapped/ws")
	h.SetRelayFailed("wss://flapped/ws", errors.New("EOF"))

	byURL := map[string]protocol.RelayState{}
	for _, r := range h.RelayHealth().Relays {
		byURL[r.URL] = r
	}
	if byURL["wss://never/ws"].LastOKAt != 0 {
		t.Error("a relay that never registered reports a last-success time")
	}
	if byURL["wss://flapped/ws"].LastOKAt == 0 {
		t.Error("a relay that registered and then dropped lost its last-success time — the app " +
			"cannot distinguish a blip from a relay that has never worked")
	}
}

// A success clears the failure trail, or the count grows forever across an outage and recovery.
func TestRecoveryClearsTheFailureTrail(t *testing.T) {
	h := New()
	for i := 0; i < 5; i++ {
		h.SetRelayFailed("wss://a/ws", errors.New("refused"))
	}
	if n := h.RelayHealth().Relays[0].Failures; n != 5 {
		t.Fatalf("failures = %d, want 5", n)
	}
	h.SetRelayConnected("wss://a/ws")
	got := h.RelayHealth().Relays[0]
	if got.Failures != 0 || got.Detail != "" || !got.Connected {
		t.Fatalf("after recovery: %+v — a recovered relay still reporting its old error reads as "+
			"still broken", got)
	}
}

// The broadcast fires on TRANSITIONS, not on every retry.
//
// The registration loop retries on a backoff starting at one second. Emitting a frame per attempt
// would send a status update to every connected device, forever, for a relay that is simply
// unreachable — and the attempt count behind a steady "down" is not something anyone acts on.
func TestRepeatedFailuresDoNotSpamEveryDevice(t *testing.T) {
	h := New()
	conn, frames := observerConn(t, h)
	_ = conn

	h.SetRelayFailed("wss://a/ws", errors.New("refused"))
	first := countRelayFrames(frames)
	if first != 1 {
		t.Fatalf("the first failure produced %d frames, want exactly 1", first)
	}
	for i := 0; i < 20; i++ {
		h.SetRelayFailed("wss://a/ws", errors.New("refused"))
	}
	if n := countRelayFrames(frames); n != 0 {
		t.Fatalf("20 further identical failures produced %d more frames. The retry loop runs at a "+
			"one-second backoff, so this is an unbounded stream of status updates to every device "+
			"about a relay whose state has not changed.", n)
	}

	// But a real transition still reaches them.
	h.SetRelayConnected("wss://a/ws")
	if n := countRelayFrames(frames); n != 1 {
		t.Fatalf("recovery produced %d frames, want 1 — the device is still showing the outage", n)
	}
}

// A changed REASON is a transition worth sending, even while the relay stays down: "connection
// refused" and "no such host" send someone to different places.
func TestAChangedReasonIsReported(t *testing.T) {
	h := New()
	_, frames := observerConn(t, h)
	h.SetRelayFailed("wss://a/ws", errors.New("connect: connection refused"))
	_ = countRelayFrames(frames)
	h.SetRelayFailed("wss://a/ws", errors.New("lookup relay.example: no such host"))
	if n := countRelayFrames(frames); n != 1 {
		t.Fatalf("a changed failure reason produced %d frames, want 1", n)
	}
}

// observerConn registers a bare client whose outbound queue the test can read, which is how a real
// device receives a broadcast. Nothing is dialled — the conn is only ever a map key here.
func observerConn(t *testing.T, h *Hub) (*transport.Conn, chan []byte) {
	t.Helper()
	conn := &transport.Conn{}
	ch := make(chan []byte, 256)
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[*transport.Conn]*hubClient{}
	}
	h.clients[conn] = &hubClient{conn: conn, ch: ch, done: make(chan struct{})}
	h.mu.Unlock()
	return conn, ch
}

// countRelayFrames drains the queue and reports how many relay.health frames were waiting.
func countRelayFrames(ch chan []byte) int {
	n := 0
	for {
		select {
		case raw := <-ch:
			if env, err := protocol.Decode(raw); err == nil && env.Type == protocol.TypeRelayHealth {
				n++
			}
		default:
			return n
		}
	}
}
