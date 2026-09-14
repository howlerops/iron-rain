package relay

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A slow client must not lose frames.
//
// The relay reads the host socket continuously — it has to, or an idle host cannot answer a ping —
// and hands what it reads to the bridge through a 32-slot channel. That send used to drop on
// overflow, justified by "nothing is paired yet". But the reader keeps running AFTER pairing, and
// after pairing every frame is live session ciphertext; the buffer fills precisely when the client is
// slow, which for a phone on cellular is the ordinary case rather than the exceptional one.
//
// Nothing downstream could catch it. The transport's replay check rejects a sequence number that goes
// BACKWARDS and says so in its own comment — a gap reads as perfectly valid. So frames vanished out
// of the middle of an encrypted stream with no error on either side.
//
// The worst single frame to lose is the first one after connect: Hub.Serve delivers a freshly minted
// per-device credential there, and the daemon has already written that credential's hash to disk. Lose
// it and the phone can never reconnect — the owner has to pair from scratch, while the log says the
// credential was delivered.
func TestASlowClientLosesNoFrames(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	host, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=backpressure&role=host", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.CloseNow()

	client, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=backpressure&role=client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	client.SetReadLimit(8 * 1024 * 1024) // the relay's own limit; the dial default is 32 KiB

	// Enough frames to overrun the 32-slot buffer many times over, each big enough that the write
	// cannot be absorbed by a socket buffer.
	const frames = 200
	payload := make([]byte, 64*1024)

	sendErr := make(chan error, 1)
	go func() {
		for i := 0; i < frames; i++ {
			binary.BigEndian.PutUint64(payload[:8], uint64(i)) // each frame carries its own index
			if err := host.Write(ctx, websocket.MessageBinary, payload); err != nil {
				sendErr <- fmt.Errorf("host write %d: %w", i, err)
				return
			}
		}
		sendErr <- nil
	}()

	// The whole point: read SLOWLY. This is the condition that used to trigger the drop.
	for i := 0; i < frames; i++ {
		rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
		_, data, err := client.Read(rctx)
		rcancel()
		if err != nil {
			t.Fatalf("frame %d never arrived: %v — the relay dropped it and neither side was told", i, err)
		}
		if got := binary.BigEndian.Uint64(data[:8]); got != uint64(i) {
			t.Fatalf("received frame %d where %d was expected: the stream has a HOLE in it, and the "+
				"transport's replay check cannot see a gap — it only rejects a sequence number that "+
				"goes backwards", got, i)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
}

// A second client must be refused, not parked forever.
//
// serveHost receives from the pairing channel exactly once. With that channel buffered, a second
// client's send SUCCEEDED into the buffer and the goroutine then blocked on a `done` nobody would
// ever close. Its socket was never read again either, so a peer that had already disappeared was
// never noticed — an unauthenticated caller holding any live server_id (a value that is printed on
// daemon start, embedded in every pairing QR, and in the relay operator's logs) could leak a
// goroutine and a socket per dial, without limit.
//
// Refusing is also the honest answer for the app. The WebSocket upgrade has already succeeded by this
// point, so a route that is silently dead is indistinguishable from one that is merely slow, and the
// client's LAN-vs-relay race would pick it and then wait out its entire handshake budget.
func TestASecondClientIsRefusedRatherThanParked(t *testing.T) {
	r := New()
	r.pairTimeout = 300 * time.Millisecond // the real one is seconds; shorten so the test is quick
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	host, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=onlyone&role=host", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.CloseNow()
	go func() { // a real daemon reads while parked
		for {
			if _, _, err := host.Read(ctx); err != nil {
				return
			}
		}
	}()

	first, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=onlyone&role=client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()

	// Prove the first client really is bridged before testing the second — otherwise "the second one
	// was refused" could just mean neither ever paired.
	if err := host.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	if _, got, err := first.Read(rctx); err != nil || string(got) != "hello" {
		rcancel()
		t.Fatalf("the first client was never bridged (%q, %v) — the rest of this test would prove nothing", got, err)
	}
	rcancel()

	second, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=onlyone&role=client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.CloseNow()

	// It must be CLOSED, and within the pairing bound. Before the fix this read blocked until the
	// test's own deadline: the relay kept the socket and the goroutine indefinitely.
	rctx, rcancel = context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	_, _, err = second.Read(rctx)
	if err == nil {
		t.Fatal("the second client received a frame — it should not have been paired at all")
	}
	if rctx.Err() != nil {
		t.Fatal("the second client was still parked when the test gave up: the relay is holding a " +
			"goroutine and an unread socket that nothing will ever reclaim")
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusTryAgainLater {
		t.Errorf("close status = %v, want %v — the client has to be able to tell "+
			"\"busy, try another route\" from a route that is simply broken", status, websocket.StatusTryAgainLater)
	}
}
