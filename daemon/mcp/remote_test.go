package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRemote serves the MCP shapes over HTTP, optionally demanding a credential.
func fakeRemote(t *testing.T, requireAuth bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireAuth && r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "server/discover":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
		case "initialize":
			reply(map[string]any{
				"protocolVersion": ProtocolLegacy,
				"serverInfo":      map[string]any{"name": "hosted", "version": "2.0"},
			})
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{{"name": "search", "description": "Search"}}})
		default:
			reply(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRemoteProbe: a hosted server must work, not be accepted-then-silently-fail as before.
func TestRemoteProbe(t *testing.T) {
	srv := fakeRemote(t, false)
	info, err := DialRemote(srv.URL, nil).Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Name != "hosted" || info.ProtocolVersion != ProtocolLegacy {
		t.Errorf("unexpected info %+v", info)
	}
	if len(info.Tools) != 1 || info.Tools[0].Name != "search" {
		t.Errorf("tools = %+v", info.Tools)
	}
}

// TestRemoteAuthFailureIsLegible: a rejected credential is the most common hosted-server failure, so
// it must not surface as a generic rpc error that sends the user hunting in the wrong place.
func TestRemoteAuthFailureIsLegible(t *testing.T) {
	srv := fakeRemote(t, true)
	_, err := DialRemote(srv.URL, nil).Probe(context.Background())
	if err == nil {
		t.Fatal("an unauthenticated probe must fail")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("error should name the real cause, got %q", err)
	}
	// With the header it succeeds.
	if _, err := DialRemote(srv.URL, map[string]string{"Authorization": "Bearer secret"}).Probe(context.Background()); err != nil {
		t.Fatalf("authenticated probe failed: %v", err)
	}
}

// TestRegistryChecksRemoteServers: Check must actually probe a hosted server now.
func TestRegistryChecksRemoteServers(t *testing.T) {
	srv := fakeRemote(t, false)
	r := NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	if err := r.Upsert(Server{Name: "hosted", Transport: "http", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	st := r.Check(context.Background(), "hosted")
	if !st.OK {
		t.Fatalf("a reachable hosted server should check OK, got %+v", st)
	}
	if len(st.Tools) != 1 {
		t.Errorf("tools should be listed, got %+v", st.Tools)
	}
}

// TestGatewayProxiesRemoteServer closes the loop: the gateway must serve hosted servers too, not
// just stdio ones.
func TestGatewayProxiesRemoteServer(t *testing.T) {
	upstream := fakeRemote(t, false)
	r := NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	if err := r.Upsert(Server{Name: "hosted", Transport: "http", URL: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(r)
	defer mgr.Shutdown()
	gw := httptest.NewServer(NewGateway(mgr, ""))
	defer gw.Close()

	resp, out := rpc(t, gw, "/mcp/hosted", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	result, _ := out["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected the hosted server's tool through the gateway, got %v", out)
	}
}

func TestFirstSSEData(t *testing.T) {
	body := []byte("event: message\ndata: {\"result\":{}}\n\n")
	if got := string(firstSSEData(body)); got != `{"result":{}}` {
		t.Errorf("firstSSEData = %q", got)
	}
	// A plain JSON body passes through untouched.
	if got := string(firstSSEData([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("non-SSE body should pass through, got %q", got)
	}
}
