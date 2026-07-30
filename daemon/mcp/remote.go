package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Remote (streamable-HTTP) MCP servers.
//
// stdio covers almost everything published today, but hosted servers are how vendors ship their own
// integrations, and they are the only kind that can't be run locally at all. Without this a remote
// entry in the registry was accepted, stored, and then quietly failed at the gateway — worse than
// refusing it outright.
//
// The shape differs from stdio in one way that matters: there is no long-lived process, so there is
// nothing to supervise and nothing to restart. Each call is an HTTP round trip. What the client does
// carry is the negotiated protocol version and the auth headers.

// remoteHTTPTimeout bounds one round trip to a hosted server.
const remoteHTTPTimeout = 60 * time.Second

// RemoteClient talks to a streamable-HTTP MCP server.
type RemoteClient struct {
	url     string
	headers map[string]string
	http    *http.Client
	nextID  atomic.Int64
}

// DialRemote returns a client for a hosted server. It performs no I/O — the first call does.
func DialRemote(url string, headers map[string]string) *RemoteClient {
	return &RemoteClient{
		url:     url,
		headers: headers,
		http:    &http.Client{Timeout: remoteHTTPTimeout},
	}
}

// call issues one JSON-RPC request over HTTP.
func (c *RemoteClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The spec asks for both JSON and SSE in Accept; we only handle a JSON reply, but sending the
	// documented Accept keeps servers that branch on it from rejecting the request outright.
	req.Header.Set("Accept", "application/json, text/event-stream")
	// Routing headers required by the 2026-07-28 revision so load balancers can route without
	// inspecting the body. Harmless to older servers.
	req.Header.Set("Mcp-Method", method)
	req.Header.Set("Mcp-Name", "iron-rain")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Say this plainly: it is the single most common failure for a hosted server, and "rpc error"
		// would send the user hunting in the wrong place.
		return nil, fmt.Errorf("the server rejected our credentials (HTTP %d) — check the Authorization header", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, c.url, firstLine(string(raw)))
	}
	// A server may answer an SSE stream even when we asked for JSON; take the first data frame,
	// which for a single-response call is the whole answer.
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		raw = firstSSEData(raw)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable reply from %s: %w", c.url, err)
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// Probe negotiates and lists tools, mirroring the stdio client's contract.
func (c *RemoteClient) Probe(ctx context.Context) (ServerInfo, error) {
	info, err := c.negotiate(ctx)
	if err != nil {
		return ServerInfo{}, err
	}
	params := map[string]any{}
	if info.ProtocolVersion == ProtocolLatest {
		params["_meta"] = metaFor(info.ProtocolVersion)
	}
	raw, err := c.call(ctx, "tools/list", params)
	if err != nil {
		return info, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	_ = json.Unmarshal(raw, &out)
	info.Tools = out.Tools
	return info, nil
}

// negotiate mirrors the stdio dual-stack probe: newest revision first, then the legacy handshake.
func (c *RemoteClient) negotiate(ctx context.Context) (ServerInfo, error) {
	if raw, err := c.call(ctx, "server/discover", map[string]any{"_meta": metaFor(ProtocolLatest)}); err == nil {
		var d struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			SupportedVersions []string `json:"supportedVersions"`
		}
		_ = json.Unmarshal(raw, &d)
		v := ProtocolLatest
		if len(d.SupportedVersions) > 0 && !contains(d.SupportedVersions, ProtocolLatest) {
			v = d.SupportedVersions[0]
		}
		return ServerInfo{Name: d.ServerInfo.Name, Version: d.ServerInfo.Version, ProtocolVersion: v}, nil
	}
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolLegacy,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "iron-rain", "version": "1"},
	})
	if err != nil {
		return ServerInfo{}, err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(raw, &init)
	v := init.ProtocolVersion
	if v == "" {
		v = ProtocolLegacy
	}
	return ServerInfo{Name: init.ServerInfo.Name, Version: init.ServerInfo.Version, ProtocolVersion: v}, nil
}

// firstSSEData extracts the first `data:` payload from an SSE body.
func firstSSEData(b []byte) []byte {
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			return []byte(strings.TrimSpace(rest))
		}
	}
	return b
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
