package wsmsg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestWriteMsgHonorsDeadline proves a WebSocket write is bounded by the per-message
// deadline (so a stalled/half-open peer can't park the writer forever) and that a timed-
// out write drops the peer. The peer here accepts the socket and never reads; a tiny
// write timeout forces the deadline path deterministically without depending on TCP
// send-buffer sizes.
func TestWriteMsgHonorsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		<-r.Context().Done() // hold the socket open but never read from it
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+srv.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.writeTimeout = 250 * time.Millisecond // short, real deadline

	// A large payload to a peer that never reads fills the socket buffers and genuinely
	// blocks the write — so the only way WriteMsg returns is the deadline firing. It must
	// return an error well before the 3s bound (not park the goroutine forever).
	big := make([]byte, 16<<20) // 16 MiB, exceeds any socket send/recv buffer
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- c.WriteMsg(big) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("WriteMsg should have failed under the write deadline")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("WriteMsg took %v; deadline not enforced", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WriteMsg blocked past its deadline")
	}

	// The timed-out write drops the peer, so the connection is now closed.
	if c.ctx.Err() == nil {
		if err := c.WriteMsg([]byte("again")); err == nil {
			t.Fatal("expected the connection to be closed after a write timeout")
		}
	}
}
