package hub

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Fetching a session's dev server on behalf of a client that cannot reach it.
//
// Design Mode renders the running app in a WKWebView — but that web view lives in the APP, and the
// dev server lives on the daemon host. On a Mac running both they happen to coincide, which is why
// this was never noticed. On a phone they do not: `localhost` is the phone, and the phone has no dev
// server, so Design Mode over the relay was pointed at nothing at all.
//
// So the daemon fetches, and the client renders what comes back. One request per sub-resource, over
// the connection that already exists. That last part is not a preference: the relay bridges exactly
// one host and one client, and a second client registration EVICTS the first, so a side-channel
// socket would work on a LAN and break the moment you were remote — precisely the case this exists
// to serve.

// previewFetchTimeout bounds one upstream request. A dev server that is compiling can take a moment
// on first hit; one that is wedged should not hold a client request open.
const previewFetchTimeout = 30 * time.Second

// maxPreviewBody caps one response body.
//
// The envelope has no chunked-reply notion, so a response crosses the wire whole, and the frame
// ceiling is 8 MiB. Base64 inflates by 4/3, which puts the real limit near 6 MiB — this sits under
// it with room for the rest of the envelope. Oversized assets are REFUSED rather than truncated: a
// half a JavaScript bundle is not a smaller bundle, it is a syntax error that would be blamed on the
// user's code.
const maxPreviewBody = 5 << 20

// hopByHopHeaders are connection-scoped and must not be forwarded in either direction; they describe
// the hop they arrived on, not the message.
var hopByHopHeaders = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

// previewClient issues the upstream request.
//
// Redirects are NOT followed. A dev server that 302s to another host would otherwise make the daemon
// fetch it — turning the one thing this design refuses to be (a caller-steerable proxy) back on
// through the back door. The redirect is handed to the client instead, and if the web view chooses
// to follow it that navigation comes back through the scheme handler and is judged on its own.
var previewClient = &http.Client{
	Timeout: previewFetchTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// previewTarget resolves the session's OWN dev server into a loopback URL plus the Host header the
// preview router routes on.
//
// Addressed as 127.0.0.1:<port> with an explicit Host rather than by dialling the *.localhost name,
// because that name is a macOS resolver behaviour. The daemon also ships for Linux, where resolving
// an arbitrary *.localhost label is not guaranteed — going straight to loopback and carrying the
// label in the header keeps routing identical on both.
func (h *Hub) previewTarget(sessionID, path string) (target string, host string, err error) {
	if h.preview == nil {
		return "", "", fmt.Errorf("previews are not running")
	}
	raw := h.preview.URL(sessionID)
	if raw == "" {
		return "", "", fmt.Errorf("this session has no dev server running")
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("preview address is unreadable: %w", perr)
	}
	port := u.Port()
	if port == "" {
		return "", "", fmt.Errorf("preview address has no port")
	}
	if _, cerr := strconv.Atoi(port); cerr != nil {
		return "", "", fmt.Errorf("preview address has a bad port")
	}

	// The path MUST begin with a slash before it is concatenated. Without that, a path of
	// "@evil.com/" produces "http://127.0.0.1:7777@evil.com/", which parses with 127.0.0.1:7777 as
	// USERINFO and evil.com as the host — the one string that turns this back into an open proxy.
	// Forcing the leading slash makes every input land under the loopback authority.
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "http://127.0.0.1:" + port + path, u.Host, nil
}

// handlePreviewFetch performs one upstream request and returns the whole response.
func (h *Hub) handlePreviewFetch(req protocol.PreviewFetchReq) (protocol.PreviewFetchResp, error) {
	var zero protocol.PreviewFetchResp

	target, host, err := h.previewTarget(req.SessionID, req.Path)
	if err != nil {
		return zero, err
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return zero, fmt.Errorf("method %q is not allowed", method)
	}

	var body io.Reader
	if req.Body != "" {
		raw, derr := base64.StdEncoding.DecodeString(req.Body)
		if derr != nil {
			return zero, fmt.Errorf("request body is not valid base64")
		}
		if len(raw) > maxPreviewBody {
			return zero, fmt.Errorf("request body is too large")
		}
		body = bytes.NewReader(raw)
	}

	hreq, err := http.NewRequest(method, target, body)
	if err != nil {
		return zero, err
	}
	// The preview router routes on Host, and Vite compares it against allowedHosts, so the label has
	// to survive even though the connection goes to a bare loopback address.
	hreq.Host = host
	for k, v := range req.Headers {
		if hopByHopHeaders[strings.ToLower(k)] || strings.EqualFold(k, "host") {
			continue
		}
		hreq.Header.Set(k, v)
	}

	resp, err := previewClient.Do(hreq)
	if err != nil {
		return zero, fmt.Errorf("the dev server did not answer: %w", err)
	}
	defer resp.Body.Close()

	// Read one byte past the cap so a body sitting exactly at the limit is not misreported.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPreviewBody+1))
	if err != nil {
		return zero, fmt.Errorf("reading the response failed: %w", err)
	}
	if len(raw) > maxPreviewBody {
		return zero, fmt.Errorf("that resource is larger than %d MiB, which is more than one message can carry", maxPreviewBody>>20)
	}

	out := protocol.PreviewFetchResp{
		Status:  resp.StatusCode,
		Headers: map[string]string{},
		Body:    base64.StdEncoding.EncodeToString(raw),
	}
	for k, vs := range resp.Header {
		if hopByHopHeaders[strings.ToLower(k)] || len(vs) == 0 {
			continue
		}
		// Content-Length would describe the pre-base64 body and the client reconstructs its own, so
		// forwarding it invites a mismatch.
		if strings.EqualFold(k, "content-length") {
			continue
		}
		out.Headers[k] = vs[0]
	}
	return out, nil
}
