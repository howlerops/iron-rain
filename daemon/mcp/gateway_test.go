package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func gatewayFixture(t *testing.T, mode, token string) (*httptest.Server, *Manager) {
	t.Helper()
	s := fakeServer(t, mode)
	reg := NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	if err := reg.Upsert(s); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(reg)
	t.Cleanup(mgr.Shutdown)
	srv := httptest.NewServer(NewGateway(mgr, token))
	t.Cleanup(srv.Close)
	return srv, mgr
}

func rpc(t *testing.T, srv *httptest.Server, path, token, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

// TestGatewayProxiesToolsList is the core promise: a harness talks HTTP to the daemon and reaches
// the ONE supervised stdio server, instead of spawning its own copy.
func TestGatewayProxiesToolsList(t *testing.T) {
	srv, _ := gatewayFixture(t, "legacy", "tok")
	resp, out := rpc(t, srv, "/mcp/fake", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, out)
	}
	if e, ok := out["error"]; ok {
		t.Fatalf("unexpected rpc error: %v", e)
	}
	result, _ := out["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected the upstream server's 2 tools, got %v", result)
	}
}

// TestGatewayRequiresToken: loopback is shared with every other process on the machine, so "local"
// is not by itself an authorization.
func TestGatewayRequiresToken(t *testing.T) {
	srv, _ := gatewayFixture(t, "legacy", "tok")
	resp, _ := rpc(t, srv, "/mcp/fake", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token → status %d, want 401", resp.StatusCode)
	}
	resp, _ = rpc(t, srv, "/mcp/fake", "wrong", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token → status %d, want 401", resp.StatusCode)
	}
}

// TestGatewayReusesOneConnection: two calls must share a single upstream process. That reuse IS the
// feature — otherwise this is just a slower way to spawn per-harness copies.
func TestGatewayReusesOneConnection(t *testing.T) {
	srv, mgr := gatewayFixture(t, "legacy", "tok")
	for i := 0; i < 3; i++ {
		if resp, out := rpc(t, srv, "/mcp/fake", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); resp.StatusCode != 200 {
			t.Fatalf("call %d failed: %v", i, out)
		}
	}
	mgr.mu.Lock()
	n := len(mgr.conns)
	mgr.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected exactly 1 supervised connection, got %d", n)
	}
}

// TestGatewayUnknownAndDisabledServers surface as JSON-RPC errors, not 404 HTML a client can't read.
func TestGatewayUnknownAndDisabledServers(t *testing.T) {
	srv, mgr := gatewayFixture(t, "legacy", "tok")
	_, out := rpc(t, srv, "/mcp/nope", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if _, ok := out["error"]; !ok {
		t.Errorf("unknown server should return a JSON-RPC error, got %v", out)
	}
	if err := mgr.reg.SetEnabled("fake", false); err != nil {
		t.Fatal(err)
	}
	mgr.Close("fake")
	_, out = rpc(t, srv, "/mcp/fake", "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	errObj, ok := out["error"].(map[string]any)
	if !ok || !strings.Contains(errObj["message"].(string), "disabled") {
		t.Errorf("disabled server should say so, got %v", out)
	}
}

// TestGatewayNotificationsAreAccepted: a notification has no id and expects no reply, so forwarding
// it would strand the upstream answer.
func TestGatewayNotificationsAccepted(t *testing.T) {
	srv, _ := gatewayFixture(t, "legacy", "tok")
	resp, _ := rpc(t, srv, "/mcp/fake", "tok", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification → status %d, want 202", resp.StatusCode)
	}
}

// TestGatewayToolCallHook: every tools/call transits the daemon, which is what makes auditing
// possible at all.
func TestGatewayToolCallHook(t *testing.T) {
	s := fakeServer(t, "legacy")
	reg := NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	if err := reg.Upsert(s); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(reg)
	defer mgr.Shutdown()
	gw := NewGateway(mgr, "")
	var gotServer, gotTool string
	gw.SetToolCallHook(func(server, tool string) { gotServer, gotTool = server, tool })
	srv := httptest.NewServer(gw)
	defer srv.Close()

	// The fake server answers tools/list for any method it knows; use tools/call's params shape.
	rpc(t, srv, "/mcp/fake", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"name":"echo"}}`)
	if gotServer != "" {
		t.Fatal("the hook must fire only for tools/call")
	}
	// A real tools/call: the fake replies "method not found", but the hook should not fire on error.
	rpc(t, srv, "/mcp/fake", "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo"}}`)
	if gotServer != "" || gotTool != "" {
		t.Errorf("hook fired for a FAILED call (%s/%s) — the audit trail would show calls that never ran", gotServer, gotTool)
	}
}

func TestWithMetaPreservesCallerParams(t *testing.T) {
	got := withMeta(json.RawMessage(`{"name":"echo","arguments":{"x":1}}`), ProtocolLatest)
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["name"] != "echo" || obj["arguments"] == nil {
		t.Errorf("caller params must survive: %v", obj)
	}
	if _, ok := obj["_meta"]; !ok {
		t.Error("_meta must be injected for the newest revision")
	}
	// A caller that already sent _meta keeps theirs.
	got = withMeta(json.RawMessage(`{"_meta":{"mine":true}}`), ProtocolLatest)
	_ = json.Unmarshal(got, &obj)
	meta, _ := obj["_meta"].(map[string]any)
	if meta["mine"] != true {
		t.Errorf("caller-supplied _meta must not be overwritten: %v", obj)
	}
	// Non-object params are left untouched rather than corrupted.
	if string(withMeta(json.RawMessage(`[1,2]`), ProtocolLatest)) != `[1,2]` {
		t.Error("array params must pass through unchanged")
	}
}

// The machine-wide token is an allow-all credential: it resolves to no session, so no mode, rule or
// approval can attach to it. That is tolerable for the user's own tooling on the same machine and
// not tolerable from the network — and this handler IS on the network, because every shipped install
// passes --addr 0.0.0.0:6000 so the pairing QR can carry the LAN IP.
func TestMachineTokenIsRefusedFromOffMachine(t *testing.T) {
	gw := NewGateway(NewManager(NewRegistry(filepath.Join(t.TempDir(), "s.json"))), "machine-token")

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write"}}`)
	req := httptest.NewRequest("POST", "/mcp/files", body)
	req.Header.Set("Authorization", "Bearer machine-token")
	req.RemoteAddr = "192.168.1.50:51234" // another host on the LAN
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("a LAN caller presenting the machine token got %d, want 403", w.Code)
	}
}

// The same credential from the same machine still works — this confines the token, it does not
// retire it, and the user's local tooling depends on it.
func TestMachineTokenStillWorksOnLoopback(t *testing.T) {
	gw := NewGateway(NewManager(NewRegistry(filepath.Join(t.TempDir(), "s.json"))), "machine-token")

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write"}}`)
	req := httptest.NewRequest("POST", "/mcp/files", body)
	req.Header.Set("Authorization", "Bearer machine-token")
	req.RemoteAddr = "127.0.0.1:51234"
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("loopback use of the machine token must keep working")
	}
}

// A per-session token stays usable remotely: it carries policy and is answerable to it, which is
// the whole distinction being drawn.
func TestSessionTokenIsAllowedFromOffMachine(t *testing.T) {
	gw := NewGateway(NewManager(NewRegistry(filepath.Join(t.TempDir(), "s.json"))), "machine-token")
	gw.AddSessionToken("session-token")

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write"}}`)
	req := httptest.NewRequest("POST", "/mcp/files", body)
	req.Header.Set("Authorization", "Bearer session-token")
	req.RemoteAddr = "192.168.1.50:51234"
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("a session token must remain usable from off-machine — the relay depends on it")
	}
}

// A RemoteAddr that cannot be parsed must fail CLOSED: this decides whether to hand over the
// allow-all credential.
func TestUnparseableRemoteAddrIsNotTreatedAsLoopback(t *testing.T) {
	for _, addr := range []string{"", "garbage", "not-an-ip:1", "example.com:443"} {
		if isLoopbackAddr(addr) {
			t.Errorf("isLoopbackAddr(%q) = true; an unreadable address must not be trusted", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:5", "[::1]:5", "127.0.0.1"} {
		if !isLoopbackAddr(addr) {
			t.Errorf("isLoopbackAddr(%q) = false; this is loopback", addr)
		}
	}
}
