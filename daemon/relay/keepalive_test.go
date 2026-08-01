package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
)

// serveUntilDead mimics server.Server.ServeConn from the keepalive's point of view: it
// parks in ReadMsg until the connection dies, which is where the daemon spends ~100% of a
// registered host socket's life.
func serveUntilDead(_ context.Context, mc transport.MsgConn) error {
	for {
		if _, err := mc.ReadMsg(); err != nil {
			return err
		}
	}
}

// TestServeHostKeepaliveTearsDownHalfOpenRelay is the daemon-side liveness guarantee from
// the roadmap: when the relay socket goes half-open, ServeHost must RETURN — that return is
// what drives relayHost's re-dial loop in main.go, so re-registration happens in seconds
// instead of at TCP-keepalive timescales (or never).
//
// The fake relay here accepts the WebSocket and then never reads it, so it never answers a
// ping: a half-open socket that looks perfectly healthy to the write path.
func TestServeHostKeepaliveTearsDownHalfOpenRelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		<-r.Context().Done()
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	served := make(chan error, 1)
	go func() {
		served <- ServeHostKeepalive(ctx, wsURL, "srv-half-open",
			KeepaliveConfig{Interval: 50 * time.Millisecond, Timeout: 250 * time.Millisecond},
			serveUntilDead)
	}()

	select {
	case err := <-served:
		if err == nil {
			t.Fatal("ServeHost returned nil; a torn-down relay socket must surface as an error so relayHost re-dials")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHost never returned: the host socket has no keepalive, so a half-open relay leaves the daemon unreachable")
	}
}

// TestServeHostKeepaliveKeepsResponsiveRelayAlive is the negative control: a relay that
// answers pings must not be torn down. Without this, "tear down on ping failure" could be
// satisfied by tearing down unconditionally.
func TestServeHostKeepaliveKeepsResponsiveRelayAlive(t *testing.T) {
	r := New()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	defer srv.CloseClientConnections()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	served := make(chan error, 1)
	go func() {
		served <- ServeHostKeepalive(ctx, wsURL, "srv-live",
			KeepaliveConfig{Interval: 20 * time.Millisecond, Timeout: time.Second},
			serveUntilDead)
	}()

	// Many ping intervals against a real relay, which pongs from its own read loop.
	select {
	case err := <-served:
		t.Fatalf("keepalive tore down a responsive relay socket: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
}

// TestDefaultKeepaliveMatchesSpec pins the shipped constants — ServeHost (the signature
// main.go calls) delegates to ServeHostKeepalive with these, so if they drift the
// behaviour proven above stops applying to production.
func TestDefaultKeepaliveMatchesSpec(t *testing.T) {
	if got := DefaultKeepalive.Interval; got < 20*time.Second || got > 30*time.Second {
		t.Fatalf("ping interval %v is outside the ~25s the roadmap specifies", got)
	}
	if DefaultKeepalive.Timeout <= 0 || DefaultKeepalive.Timeout >= DefaultKeepalive.Interval {
		t.Fatalf("pong deadline %v must be positive and shorter than the interval %v",
			DefaultKeepalive.Timeout, DefaultKeepalive.Interval)
	}
}
