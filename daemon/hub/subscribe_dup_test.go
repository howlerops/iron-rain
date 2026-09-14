package hub

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/transport"
)

// A frame broadcast while a subscriber's replay is being assembled must arrive exactly once.
//
// subscribe registered the subscriber, released m.mu, and only THEN snapshotted the replay. A frame
// broadcast in between went out live AND landed in the snapshot, and the dedup set that would have
// caught it did not exist yet, so the client rendered it twice.
//
// ON THE SIZE OF THAT WINDOW, because the findings register overstated it. The finding said the
// window is wide for a restored session — "full SQLite read + sha256 per frame". It is not:
// fullHistory snapshots the ring under m.mu as its FIRST statement, and the durable read, the join
// and the hashing all happen after that snapshot, so a frame broadcast during them is not in the
// replay at all. The real window runs from prepareSubscription's unlock to fullHistory's lock: a few
// instructions with no I/O in them.
//
// Measured on this machine with six goroutines broadcasting flat out into it: one round in three
// hundred produced a duplicate, and zero in three hundred with the buffering in place. So the defect
// is real and the fix closes it, but no negative control at a sane runtime fails reliably — a
// regression would slip past this test about 299 times in 300. It is kept as a cheap regression net,
// NOT as evidence. The deterministic test of the mechanism is the one below.
func TestSubscribeDoesNotDeliverAFrameTwice(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}

	for round := 0; round < 15; round++ {
		sess := &forkSess{ch: make(chan agent.Event, 8), id: fmt.Sprintf("sess_%d", round)}
		m := newManagedSession(h, sess, sessionMeta{})
		for i := 0; i < 100; i++ {
			m.broadcast(encodeMessage(t, sess.ID(), fmt.Sprintf("history %d", i)))
		}

		stop := make(chan struct{})
		racing := make(chan struct{})
		go func() {
			defer close(racing)
			for i := 0; i < 200; i++ {
				select {
				case <-stop:
					return
				default:
				}
				m.broadcast(encodeMessage(t, sess.ID(), fmt.Sprintf("racer %d", i)))
			}
		}()

		s, replay, _ := m.prepareSubscription(&transport.Conn{})
		close(stop)
		<-racing

		counts := map[string]int{}
		for _, f := range replay {
			counts[string(f)]++
		}
		for _, f := range drainAll(s.ch) {
			counts[string(f)]++
		}
		for frame, n := range counts {
			if n > 1 {
				t.Fatalf("round %d: a frame was delivered %d times: %s", round, n, frame)
			}
		}
	}
}

// The mechanism itself, deterministically: while a subscriber is assembling its replay, broadcast
// must hold its frames aside rather than send them, and hand them back afterwards.
//
// This is the half that can be pinned down. It drives the real broadcast path — the same function
// the event pump calls — and asserts the branch that closes the window above.
func TestBroadcastHoldsFramesAsideDuringAReplaySnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	sess := &forkSess{ch: make(chan agent.Event, 8), id: "held"}
	m := newManagedSession(h, sess, sessionMeta{})

	conn := &transport.Conn{}
	s := &subscriber{conn: conn, ch: make(chan []byte, 64), done: make(chan struct{})}
	m.mu.Lock()
	m.subs[conn] = s
	m.mu.Unlock()

	// Live delivery, for contrast: this one goes straight out.
	before := encodeMessage(t, sess.ID(), "before the snapshot")
	m.broadcast(before)
	if got := drainAll(s.ch); len(got) != 1 || string(got[0]) != string(before) {
		t.Fatalf("a normal broadcast did not reach the subscriber (%d frames) — the rest of this test "+
			"would prove nothing", len(got))
	}

	// Now the window.
	s.startBuffering()
	during := encodeMessage(t, sess.ID(), "during the snapshot")
	m.broadcast(during)
	if got := drainAll(s.ch); len(got) != 0 {
		t.Fatalf("a frame broadcast mid-snapshot was delivered live (%d frames). It is also in the "+
			"replay being assembled, so the client receives it twice and renders the message twice.",
			len(got))
	}

	held := s.stopBuffering()
	if len(held) != 1 || string(held[0]) != string(during) {
		t.Fatalf("stopBuffering returned %d frames, want the one held aside — anything lost here is a "+
			"hole in the conversation, which is worse than the duplicate being fixed", len(held))
	}

	// And delivery resumes.
	after := encodeMessage(t, sess.ID(), "after the snapshot")
	m.broadcast(after)
	if got := drainAll(s.ch); len(got) != 1 || string(got[0]) != string(after) {
		t.Fatalf("live delivery did not resume after the snapshot (%d frames)", len(got))
	}
}

// Deduping the overlap must not turn a double delivery into a missing message: a frame that WAS in
// the replay is dropped from the held set, one that was not is kept.
func TestHeldFramesAreDedupedAgainstTheReplayNotDiscarded(t *testing.T) {
	s := &subscriber{ch: make(chan []byte, 8), done: make(chan struct{})}
	inReplay := []byte(`{"type":"session.message","payload":{"text":"already in the replay"}}`)
	notInReplay := []byte(`{"type":"session.message","payload":{"text":"arrived after the snapshot"}}`)

	s.rememberReplay([][]byte{inReplay}, time.Minute)
	if !s.seen(inReplay) {
		t.Fatal("a frame present in the replay was not recognised as already delivered")
	}
	if s.seen(notInReplay) {
		t.Fatal("a frame absent from the replay was suppressed — that is a hole in the conversation")
	}
}

func encodeMessage(t *testing.T, sid, text string) []byte {
	t.Helper()
	raw, err := (agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
		SessionID: sid, Role: "assistant", Text: text}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// drainAll reads everything queued for a subscriber, waiting briefly for stragglers.
func drainAll(ch chan []byte) [][]byte {
	var out [][]byte
	for {
		select {
		case raw := <-ch:
			out = append(out, raw)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}
