package preview

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// HMR and live reload run over websockets. A preview URL that broke them would be strictly worse
// than the port it replaced, so the Upgrade has to survive the proxy — including the upstream seeing
// the browser's Host, which is what Vite checks before accepting a dev-server socket.
func TestWebsocketUpgradePassesThroughTheProxy(t *testing.T) {
	var upstreamHost string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected upgrade", 400)
			return
		}
		upstreamHost = r.Host
		hj := w.(http.Hijacker)
		c, buf, _ := hj.Hijack()
		defer c.Close()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		buf.Flush()
		b := make([]byte, 64)
		n, _ := buf.Read(b)
		buf.Write(b[:n]) // echo, proving the tunnel is bidirectional
		buf.Flush()
	}))
	defer up.Close()

	r := started(t)
	r.Register("s1", "hmr app", portOf(t, up.URL))

	// Dial the PROXY, addressed by name, and perform the handshake through it.
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.Port()), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(c, "GET /ws HTTP/1.1\r\nHost: hmr-app.localhost:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", r.Port())

	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("no upgrade through the proxy: %q", strings.TrimSpace(status))
	}
	for { // drain headers
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "" {
			break
		}
	}
	// Bidirectional check: bytes must flow both ways after the upgrade.
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := br.Read(echo); err != nil {
		t.Fatalf("no echo after upgrade: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("echo = %q, want ping", echo)
	}
	if !strings.HasPrefix(upstreamHost, "hmr-app.localhost") {
		t.Fatalf("upstream saw Host %q on the websocket — Vite would reject it", upstreamHost)
	}
}

// A dev server restarted on a DIFFERENT port must keep its name. Vite picks 5174 when 5173 is busy,
// and a name that silently keeps pointing at the dead port is worse than no name at all.
func TestNameFollowsAServerThatMovesPort(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "old-server")
	}))
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "new-server")
	}))
	defer second.Close()

	r := started(t)
	label := r.Register("s1", "my app", portOf(t, first.URL))
	if got := get(t, r, label+".localhost:1"); got != "old-server" {
		t.Fatalf("before move: %q", got)
	}
	first.Close()

	// The poller re-registers the same session on its new port.
	if l2 := r.Register("s1", "my app", portOf(t, second.URL)); l2 != label {
		t.Fatalf("label changed on port move: %q -> %q (a bookmarked URL would break)", label, l2)
	}
	if got := get(t, r, label+".localhost:1"); got != "new-server" {
		t.Fatalf("after move: %q — the name did not follow the server", got)
	}
}

// Two sessions must never see each other's server, which is the whole reason they get separate names.
func TestNoCrossTalkBetweenSessions(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "A") }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "B") }))
	defer b.Close()

	r := started(t)
	la := r.Register("sa", "alpha", portOf(t, a.URL))
	lb := r.Register("sb", "bravo", portOf(t, b.URL))
	for i := 0; i < 20; i++ { // repeated, because a routing bug can be racy rather than constant
		if got := get(t, r, la+".localhost:1"); got != "A" {
			t.Fatalf("iteration %d: alpha served %q", i, got)
		}
		if got := get(t, r, lb+".localhost:1"); got != "B" {
			t.Fatalf("iteration %d: bravo served %q", i, got)
		}
	}
}

// Concurrent requests across many names must not interleave state.
func TestConcurrentRequestsStayOnTheirOwnRoute(t *testing.T) {
	r := started(t)
	const n = 8
	want := map[string]string{}
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("server-%d", i)
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		}))
		defer s.Close()
		label := r.Register(fmt.Sprintf("s%d", i), fmt.Sprintf("app %d", i), portOf(t, s.URL))
		want[label] = body
	}
	errs := make(chan error, n*10)
	done := make(chan struct{})
	for label, body := range want {
		go func(label, body string) {
			for i := 0; i < 10; i++ {
				if got := get(t, r, label+".localhost:1"); got != body {
					errs <- fmt.Errorf("%s served %q, want %q", label, got, body)
					break
				}
			}
			done <- struct{}{}
		}(label, body)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
