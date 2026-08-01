package relay

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestIdleHostAnswersPings is the regression for a relay that kills healthy connections.
//
// coder/websocket only services control frames INSIDE a Read. A registered host that is parked
// waiting for a client with nobody reading its socket therefore cannot answer a ping — so the
// daemon's own keepalive times out against a perfectly healthy relay, tears the connection down, and
// re-registers. Forever. Every daemon, around the clock, with a window on each cycle where a client
// asking for that daemon is told there is no host.
//
// The observation window matters: it must exceed interval + 2×timeout, or the test closes before the
// failure it exists to catch can occur.
func TestIdleHostAnswersPings(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	host, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=idle-probe&role=host", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.CloseNow()

	// The PROBE must read too. coder/websocket delivers a pong through the read path, so a Ping on a
	// socket nobody is reading can never complete — the measurement would fail for a reason that has
	// nothing to do with the relay. The daemon does read while parked (its serveConn loop), so this
	// mirrors production rather than papering over it.
	go func() {
		for {
			if _, _, err := host.Read(ctx); err != nil {
				return
			}
		}
	}()

	// Ping repeatedly with no client ever pairing — exactly what a daemon does while waiting.
	deadline := time.Now().Add(3 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		pctx, pcancel := context.WithTimeout(ctx, time.Second)
		err := host.Ping(pctx)
		pcancel()
		if err != nil {
			t.Fatalf("ping %d failed after %v: %v — an idle host must stay pingable, or its keepalive tears down a healthy connection", i, time.Since(deadline.Add(-3*time.Second)), err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Once a client pairs, frames must still flow both ways — the idle reader must hand over cleanly
// rather than swallowing the conversation.
func TestPairedBridgeStillCarriesFrames(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	host, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=bridged&role=host", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.CloseNow()
	time.Sleep(150 * time.Millisecond) // let the host register

	client, _, err := websocket.Dial(ctx, wsURL+"/ws?sid=bridged&role=client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()

	if err := client.Write(ctx, websocket.MessageBinary, []byte("to-host")); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	_, got, err := host.Read(rctx)
	if err != nil {
		t.Fatalf("host never received the client's frame: %v", err)
	}
	if string(got) != "to-host" {
		t.Errorf("host got %q, want %q", got, "to-host")
	}

	if err := host.Write(ctx, websocket.MessageBinary, []byte("to-client")); err != nil {
		t.Fatal(err)
	}
	r2, c2 := context.WithTimeout(ctx, 5*time.Second)
	defer c2()
	_, back, err := client.Read(r2)
	if err != nil {
		t.Fatalf("client never received the host's frame: %v", err)
	}
	if string(back) != "to-client" {
		t.Errorf("client got %q, want %q", back, "to-client")
	}
}
