package hub

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/preview"
	"github.com/howlerops/oculus/daemon/protocol"
)

// previewHub wires a hub to a running preview router with one session registered against `upstream`.
func previewHub(t *testing.T, upstream string) *Hub {
	t.Helper()
	h := New()
	r := preview.New()
	if err := r.Start(0); err != nil {
		t.Fatalf("start preview router: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h.preview = r

	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	r.Register("sess-1", "my-app", port)
	return h
}

// The single most important property here: the caller supplies a PATH, never a host.
//
// Concatenating an unanchored path onto "http://127.0.0.1:<port>" is how this becomes an open proxy.
// "@evil.com/" yields "http://127.0.0.1:7777@evil.com/", which parses with the loopback address as
// USERINFO and evil.com as the host — the daemon would then fetch evil.com from inside the LAN.
func TestPreviewTargetCannotBeSteeredOffLoopback(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer up.Close()
	h := previewHub(t, up.URL)

	hostile := []string{
		"@evil.com/",
		"@169.254.169.254/latest/meta-data/",
		"evil.com/",
		"//evil.com/",
		"\\\\evil.com\\",
		":8080/admin",
	}
	for _, p := range hostile {
		target, _, err := h.previewTarget("sess-1", p)
		if err != nil {
			continue // refusing outright is also a fine answer
		}
		u, perr := url.Parse(target)
		if perr != nil {
			t.Errorf("path %q produced an unparseable target %q", p, target)
			continue
		}
		if u.Hostname() != "127.0.0.1" {
			t.Errorf("path %q escaped to host %q (target %q)", p, u.Hostname(), target)
		}
		if u.User != nil {
			t.Errorf("path %q introduced userinfo, which moves the host: %q", p, target)
		}
	}
}

func TestPreviewTargetNeedsARunningPreview(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer up.Close()
	h := previewHub(t, up.URL)

	if _, _, err := h.previewTarget("no-such-session", "/"); err == nil {
		t.Fatal("a session with no dev server must not resolve to a target")
	}
}

// The preview router routes on Host, and Vite compares it against allowedHosts, so the label has to
// survive even though the connection is made to a bare loopback address.
func TestPreviewFetchPreservesTheHostHeader(t *testing.T) {
	var seenHost string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		fmt.Fprint(w, "ok")
	}))
	defer up.Close()
	h := previewHub(t, up.URL)

	resp, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status %d", resp.Status)
	}
	if !strings.HasPrefix(seenHost, "my-app.localhost") {
		t.Errorf("upstream saw Host %q, want the preview label", seenHost)
	}
}

func TestPreviewFetchReturnsBodyAndHeaders(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<h1>hello</h1>")
	}))
	defer up.Close()
	h := previewHub(t, up.URL)

	resp, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/index.html"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Body)
	if err != nil {
		t.Fatalf("body is not valid base64: %v", err)
	}
	if string(raw) != "<h1>hello</h1>" {
		t.Errorf("body = %q", raw)
	}
	if ct := resp.Headers["Content-Type"]; !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, the web view needs it to render", ct)
	}
}

// A dev server that redirects off-host must not make the daemon follow it — that would restore the
// steerable-proxy behaviour the path-only design exists to prevent.
func TestPreviewFetchDoesNotFollowRedirects(t *testing.T) {
	var reachedElsewhere bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reachedElsewhere = true
	}))
	defer elsewhere.Close()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/pwned", http.StatusFound)
	}))
	defer up.Close()
	h := previewHub(t, up.URL)

	resp, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if reachedElsewhere {
		t.Fatal("the daemon followed a redirect off-host — that is the SSRF this design refuses")
	}
	if resp.Status != http.StatusFound {
		t.Errorf("status = %d, want the 302 handed back to the client", resp.Status)
	}
	if resp.Headers["Location"] == "" {
		t.Error("the client needs Location to decide for itself")
	}
}

// Oversized assets are refused, not truncated: half a JavaScript bundle is a syntax error the user
// would blame on their own code.
func TestPreviewFetchRefusesOversizedBodies(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxPreviewBody+1024))
	}))
	defer up.Close()
	h := previewHub(t, up.URL)

	_, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/big.js"})
	if err == nil {
		t.Fatal("an oversized body must be refused rather than silently truncated")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("the error should explain the limit, got %q", err)
	}
}

// A body exactly at the cap is fine — the reader takes one byte past it precisely so this case is
// not misreported.
func TestPreviewFetchAllowsABodyAtTheLimit(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxPreviewBody))
	}))
	defer up.Close()
	h := previewHub(t, up.URL)

	if _, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/edge.js"}); err != nil {
		t.Fatalf("a body exactly at the cap should be allowed: %v", err)
	}
}

func TestPreviewFetchRejectsOddMethods(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer up.Close()
	h := previewHub(t, up.URL)

	if _, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/", Method: "TRACE"}); err == nil {
		t.Error("TRACE should not be proxied")
	}
	if _, err := h.handlePreviewFetch(protocol.PreviewFetchReq{SessionID: "sess-1", Path: "/", Method: "CONNECT"}); err == nil {
		t.Error("CONNECT should not be proxied")
	}
}

// Hop-by-hop headers describe the connection they arrived on, not the message, and forwarding them
// corrupts the next hop.
func TestPreviewFetchDropsHopByHopHeaders(t *testing.T) {
	var seen http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "x")
	}))
	defer up.Close()
	h := previewHub(t, up.URL)

	resp, err := h.handlePreviewFetch(protocol.PreviewFetchReq{
		SessionID: "sess-1",
		Path:      "/",
		Headers:   map[string]string{"Upgrade": "websocket", "Accept": "text/html"},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if seen.Get("Upgrade") != "" {
		t.Error("Upgrade must not be forwarded upstream")
	}
	if seen.Get("Accept") != "text/html" {
		t.Error("ordinary request headers must survive")
	}
	for k := range resp.Headers {
		if strings.EqualFold(k, "connection") {
			t.Error("Connection must not be returned to the client")
		}
	}
}
