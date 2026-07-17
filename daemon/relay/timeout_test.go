package relay

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestRegistrationTimeoutDropsSilentPeer proves the slowloris guard: a peer that opens
// the relay socket and never sends its registration frame is closed shortly after
// registrationTimeout instead of parking a goroutine for the life of the connection.
func TestRegistrationTimeoutDropsSilentPeer(t *testing.T) {
	r := New()
	r.regTimeout = 100 * time.Millisecond // per-instance: no shared global, no race
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// Send nothing. The relay must close us well before the 3s ctx deadline.
	start := time.Now()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("expected the relay to drop a silent (unregistered) peer")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("relay took %v to drop a silent peer; timeout not enforced", elapsed)
	}
}
