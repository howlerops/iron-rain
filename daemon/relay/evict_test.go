package relay_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/relay"
)

func dialAndRegisterHost(t *testing.T, ctx context.Context, url, serverID string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	reg, _ := json.Marshal(map[string]string{"role": "host", "server_id": serverID})
	if err := ws.Write(ctx, websocket.MessageBinary, reg); err != nil {
		t.Fatalf("write registration: %v", err)
	}
	return ws
}

// TestServeHost_DuplicateRegistrationEvictsPrevious proves that a second host
// registering with the same server_id evicts the first (closing its socket)
// rather than silently orphaning the earlier serveHost goroutine on a channel no
// client can ever reach.
func TestServeHost_DuplicateRegistrationEvictsPrevious(t *testing.T) {
	srv := httptest.NewServer(relay.New().Handler())
	defer srv.Close()
	defer srv.CloseClientConnections()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const id = "dup-1"
	host1 := dialAndRegisterHost(t, ctx, url, id)
	// Let the relay register host1 before the duplicate registration arrives.
	time.Sleep(200 * time.Millisecond)

	host2 := dialAndRegisterHost(t, ctx, url, id)
	// CloseNow avoids a graceful close handshake the relay won't answer (its
	// serveHost goroutine is parked in select, not reading host2), keeping
	// teardown fast.
	defer host2.CloseNow()

	// host1 must be evicted: the relay closed its socket, so a read errors out
	// promptly instead of the old goroutine stranding forever.
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()
	if _, _, err := host1.Read(readCtx); err == nil {
		t.Fatal("expected host1 to be evicted (read error) after duplicate registration, got nil error")
	}
}
