package wsmsg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// halfOpenServer accepts the WebSocket and then never reads from it. A peer that never
// reads never processes control frames, so it never pongs — which is exactly what a
// half-open TCP connection looks like from the sending side: writes appear to succeed
// (they land in the kernel buffer) and nothing ever comes back.
func halfOpenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		<-r.Context().Done() // hold the socket open, never read, never pong
	}))
}

// echoServer accepts the WebSocket and reads in a loop. coder/websocket answers pings
// from inside the read loop, so this peer is genuinely responsive.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		for {
			typ, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if err := ws.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
}

// TestKeepaliveTearsDownHalfOpenPeer is the core liveness guarantee: a registered socket
// whose peer has silently gone away must be detected and torn down within ~one ping
// deadline, so the blocked reader returns and the caller can re-dial. Without the
// keepalive the reader below parks forever (the daemon stays running-but-unreachable).
func TestKeepaliveTearsDownHalfOpenPeer(t *testing.T) {
	srv := halfOpenServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+srv.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	stop := c.Keepalive(40*time.Millisecond, 200*time.Millisecond)
	defer stop()

	// A real serve loop is blocked in ReadMsg. The only thing that can unblock it is the
	// keepalive tearing the connection down.
	read := make(chan error, 1)
	go func() {
		_, rerr := c.ReadMsg()
		read <- rerr
	}()

	select {
	case rerr := <-read:
		if rerr == nil {
			t.Fatal("ReadMsg returned no error; the connection should have been torn down")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("keepalive did not tear down a half-open peer: ReadMsg still blocked after 3s")
	}
}

// TestKeepaliveLeavesHealthyPeerAlone guards the other direction — a keepalive that kills
// good connections is worse than none. Several ping intervals must pass with the socket
// still usable.
func TestKeepaliveLeavesHealthyPeerAlone(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+srv.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	stop := c.Keepalive(20*time.Millisecond, time.Second)
	defer stop()

	// Drive the reader so pongs get processed, and keep the socket in use across many
	// ping intervals.
	for i := 0; i < 10; i++ {
		if err := c.WriteMsg([]byte("hello")); err != nil {
			t.Fatalf("write %d: keepalive killed a healthy connection: %v", i, err)
		}
		if _, err := c.ReadMsg(); err != nil {
			t.Fatalf("read %d: keepalive killed a healthy connection: %v", i, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestKeepaliveStopIsIdempotent: the stop func is deferred by callers and may also run on
// an error path, so calling it twice must not panic on a closed channel.
func TestKeepaliveStopIsIdempotent(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+srv.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	stop := c.Keepalive(time.Second, time.Second)
	stop()
	stop()
}
