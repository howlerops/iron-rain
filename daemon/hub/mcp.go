package hub

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// The daemon-owned MCP host, exposed over the protocol. See daemon/mcp for why the daemon owns the
// registry rather than each harness.

// SetMCPRegistry enables MCP support, backed by the registry file at path.
func (h *Hub) SetMCPRegistry(r *mcp.Registry) {
	h.mu.Lock()
	h.mcp = r
	h.mu.Unlock()
}

func (h *Hub) mcpRegistry() *mcp.Registry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mcp
}

// mcpList renders the registry + last known status for the UI.
func (h *Hub) mcpList() protocol.MCPList {
	r := h.mcpRegistry()
	if r == nil {
		return protocol.MCPList{Servers: []protocol.MCPServerInfo{}}
	}
	statuses := r.Statuses()
	servers := r.List()
	out := protocol.MCPList{Servers: make([]protocol.MCPServerInfo, 0, len(servers))}
	for _, s := range servers {
		info := protocol.MCPServerInfo{
			Name:      s.Name,
			Transport: s.Transport,
			Command:   s.Command,
			Args:      s.Args,
			Env:       redactEnv(s.Env),
			URL:       s.URL,
			Enabled:   !s.Disabled,
			ProjectID: s.ProjectID,
		}
		if st, ok := statuses[s.Name]; ok {
			info.OK, info.Error = st.OK, st.Error
			info.ProtocolVersion, info.ServerVersion = st.ProtocolVersion, st.ServerVersion
			if !st.CheckedAt.IsZero() {
				info.CheckedAt = st.CheckedAt.Unix()
			}
			for _, t := range st.Tools {
				info.Tools = append(info.Tools, protocol.MCPTool{Name: t.Name, Description: t.Description})
			}
		}
		out.Servers = append(out.Servers, info)
	}
	return out
}

// redactEnv replaces credential VALUES with a placeholder before they cross the wire. The keys are
// useful (you want to see that GITHUB_TOKEN is set); the values are secrets that have no business
// leaving the daemon, including to a phone on someone else's network.
func redactEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if v == "" {
			out[k] = ""
			continue
		}
		out[k] = "••••"
	}
	return out
}

// mcpServersForSession returns the MCP servers to inject into one session's harness.
// The token identifies the SESSION to the gateway, which is what lets approval rules and modes apply
// to MCP tool calls at all.
func (h *Hub) mcpServersForSession(projectID, token string) []mcp.Server {
	r := h.mcpRegistry()
	if r == nil {
		return nil
	}
	servers := r.Enabled(projectID)
	// The daemon's own preview tools ride the same path as any other server: declared as stdio so
	// gatewayServers rewrites them to the authenticated gateway URL, which is what makes the session
	// token — and therefore modes and approval rules — apply to them too.
	servers = append(servers, mcp.Server{Name: previewBuiltinServer, Transport: "stdio"})
	return h.gatewayServers(servers, token)
}

// handleMCP dispatches the mcp.* protocol surface. Every mutating arm broadcasts, so a second device
// (or the phone that just added a server) sees the change immediately.
func (h *Hub) handleMCP(ctx context.Context, conn *transport.Conn, env protocol.Envelope) {
	r := h.mcpRegistry()
	if r == nil {
		h.sendErr(conn, env.ID, "MCP is not enabled on this daemon")
		return
	}
	switch env.Type {
	case protocol.TypeMCPList:
		h.sendOK(conn, env.ID, h.mcpList())

	case protocol.TypeMCPUpsert:
		if !h.requireCapability(conn, env.ID, capOwner, "change MCP servers") {
			return
		}
		var in protocol.MCPUpsert
		if err := env.Unmarshal(&in); err != nil {
			h.sendErr(conn, env.ID, "bad mcp.upsert")
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			h.sendErr(conn, env.ID, "a server needs a name")
			return
		}
		transport := in.Transport
		if transport == "" {
			transport = "stdio"
		}
		if transport == "stdio" && strings.TrimSpace(in.Command) == "" {
			h.sendErr(conn, env.ID, "a stdio server needs a command")
			return
		}
		if transport == "http" && strings.TrimSpace(in.URL) == "" {
			h.sendErr(conn, env.ID, "a remote server needs a URL")
			return
		}
		// An edit that omits env keeps the stored credentials: the client only ever SEES redacted
		// values, so echoing them back would otherwise overwrite real secrets with "••••".
		srv := mcp.Server{
			Name: in.Name, Transport: transport, Command: in.Command, Args: in.Args,
			Env: in.Env, Cwd: in.Cwd, URL: in.URL, Headers: in.Headers, ProjectID: in.ProjectID,
		}
		if prev, ok := r.Get(in.Name); ok {
			srv.Disabled = prev.Disabled
			srv.Env = mergeSecrets(prev.Env, in.Env)
			srv.Headers = mergeSecrets(prev.Headers, in.Headers)
		}
		if err := r.Upsert(srv); err != nil {
			h.sendErr(conn, env.ID, "save mcp: "+err.Error())
			return
		}
		log.Printf("mcp: server %q saved (%s)", srv.Name, srv.Transport)
		h.sendOK(conn, env.ID, h.mcpList())
		h.broadcast(protocol.TypeMCPChanged, h.mcpList())
		// Probe in the background so the UI shows whether it actually works without blocking the save.
		go func() {
			r.Check(context.Background(), srv.Name)
			h.broadcast(protocol.TypeMCPChanged, h.mcpList())
		}()

	case protocol.TypeMCPDelete:
		if !h.requireCapability(conn, env.ID, capOwner, "delete an MCP server") {
			return
		}
		var ref protocol.MCPRef
		if err := env.Unmarshal(&ref); err != nil || strings.TrimSpace(ref.Name) == "" {
			h.sendErr(conn, env.ID, "bad mcp.delete")
			return
		}
		if err := r.Delete(ref.Name); err != nil {
			h.sendErr(conn, env.ID, "delete mcp: "+err.Error())
			return
		}
		log.Printf("mcp: server %q removed", ref.Name)
		h.sendOK(conn, env.ID, h.mcpList())
		h.broadcast(protocol.TypeMCPChanged, h.mcpList())

	case protocol.TypeMCPEnable:
		if !h.requireCapability(conn, env.ID, capOwner, "enable an MCP server") {
			return
		}
		var in protocol.MCPEnable
		if err := env.Unmarshal(&in); err != nil || strings.TrimSpace(in.Name) == "" {
			h.sendErr(conn, env.ID, "bad mcp.enable")
			return
		}
		if err := r.SetEnabled(in.Name, in.Enabled); err != nil {
			h.sendErr(conn, env.ID, "no such server")
			return
		}
		h.sendOK(conn, env.ID, h.mcpList())
		h.broadcast(protocol.TypeMCPChanged, h.mcpList())

	case protocol.TypeMCPDiscover:
		found := h.discoverMCP(h.discoverCwd())
		out := protocol.MCPDiscovered{Exclusive: h.mcpExclusiveEnabled()}
		for _, f := range found {
			e := protocol.MCPFound{
				Name: f.Server.Name, Transport: f.Server.Transport,
				Command: f.Server.Command, Args: f.Server.Args, URL: f.Server.URL,
				Source: f.Source, Path: f.Path,
			}
			for k := range f.Server.Env {
				e.EnvKeys = append(e.EnvKeys, k)
			}
			sort.Strings(e.EnvKeys)
			out.Found = append(out.Found, e)
		}
		h.sendOK(conn, env.ID, out)

	case protocol.TypeMCPImport:
		if !h.requireCapability(conn, env.ID, capOwner, "import MCP servers") {
			return
		}
		var in protocol.MCPImport
		if err := env.Unmarshal(&in); err != nil {
			h.sendErr(conn, env.ID, "bad mcp.import")
			return
		}
		n := h.importMCP(in.Names)
		if n == 0 {
			h.sendErr(conn, env.ID, "nothing was imported — re-scan and try again")
			return
		}
		h.sendOK(conn, env.ID, h.mcpList())
		h.broadcast(protocol.TypeMCPChanged, h.mcpList())

	case protocol.TypeMCPExclusive:
		if !h.requireCapability(conn, env.ID, capOwner, "change MCP exclusivity") {
			return
		}
		var in protocol.MCPExclusiveSet
		if err := env.Unmarshal(&in); err != nil {
			h.sendErr(conn, env.ID, "bad mcp.exclusive")
			return
		}
		h.SetMCPExclusive(in.Enabled)
		h.sendOK(conn, env.ID, protocol.MCPExclusiveSet{Enabled: in.Enabled})

	case protocol.TypeMCPBrowse:
		var req protocol.MCPBrowse
		_ = env.Unmarshal(&req)
		entries, err := mcp.BrowseDirectory(ctx, req.Query)
		if err != nil {
			// BrowseDirectory names the registry itself ("the MCP registry returned HTTP 503"), so
			// prefixing named it twice in one sentence. Pass the error through; it is already a
			// complete thought.
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := protocol.MCPDirectory{Entries: make([]protocol.MCPDirectoryEntry, 0, len(entries))}
		for _, e := range entries {
			out.Entries = append(out.Entries, protocol.MCPDirectoryEntry{
				Name: e.Name, Description: e.Description, Version: e.Version,
				Command: e.Command, Args: e.Args, URL: e.URL, Transport: e.Transport,
				EnvKeys: e.EnvKeys, Unsupported: e.Unsupported,
			})
		}
		h.sendOK(conn, env.ID, out)

	case protocol.TypeMCPCheck:
		if !h.requireCapability(conn, env.ID, capOwner, "check an MCP server") {
			return
		}
		var ref protocol.MCPRef
		if err := env.Unmarshal(&ref); err != nil || strings.TrimSpace(ref.Name) == "" {
			h.sendErr(conn, env.ID, "bad mcp.check")
			return
		}
		r.Check(ctx, ref.Name)
		h.sendOK(conn, env.ID, h.mcpList())
		h.broadcast(protocol.TypeMCPChanged, h.mcpList())
	}
}

// mergeSecrets keeps a previously-stored value whenever the incoming one is the redaction
// placeholder, so an edit made from the UI can't blank out a credential it was never shown.
func mergeSecrets(prev, incoming map[string]string) map[string]string {
	if len(incoming) == 0 {
		return prev
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if v == "••••" {
			if old, ok := prev[k]; ok {
				out[k] = old
				continue
			}
		}
		out[k] = v
	}
	return out
}

// OpenCodeMCPConfig renders the enabled global MCP servers as an OPENCODE_CONFIG_CONTENT document.
//
// opencode's server is started ONCE at daemon boot and shared by every session, so unlike the other
// harnesses its MCP set can't be per-project — only project-unscoped servers are injected. A
// project-scoped server simply doesn't reach opencode; that's an honest limit of a shared server,
// not something to paper over by leaking one project's tools into another.
func (h *Hub) OpenCodeMCPConfig() string {
	r := h.mcpRegistry()
	if r == nil {
		return ""
	}
	var global []mcp.Server
	for _, s := range r.List() {
		if !s.Disabled && s.ProjectID == "" {
			global = append(global, s)
		}
	}
	return mcp.OpenCodeConfigJSON(h.gatewayServers(global, ""))
}

// SetMCPGateway records the local gateway so injected configs point harnesses at it instead of at a
// per-harness copy of each server.
func (h *Hub) SetMCPGateway(g *mcp.Gateway, token string) {
	h.mu.Lock()
	h.mcpGateway = g
	h.mcpToken = token
	h.mu.Unlock()
	g.SetToolCallHook(func(server, tool string) {
		// Every MCP tool call transits the daemon, so this is the one place it can be recorded.
		log.Printf("mcp: tool call %s/%s", server, tool)
	})
	// MCP tools obey the same modes and approval rules as native ones (see mcp_authz.go).
	g.SetAuthorizer(h.authorizeMCPTool)
}

// SetMCPGatewayBase records the reachable base URL of the local gateway (set once the listener is up).
func (h *Hub) SetMCPGatewayBase(base string) {
	h.mu.Lock()
	h.mcpGatewayBase = base
	h.mu.Unlock()
}

// gatewayServers rewrites stdio server definitions into REMOTE ones pointing at the local gateway,
// so each harness talks HTTP to the daemon and the actual server process runs exactly once.
//
// Falls back to the raw stdio definitions when the gateway isn't up — a harness with its own copy of
// a server is strictly better than a harness with no tools at all.
func (h *Hub) gatewayServers(servers []mcp.Server, token string) []mcp.Server {
	h.mu.Lock()
	g, base := h.mcpGateway, h.mcpGatewayBase
	if token == "" {
		token = h.mcpToken
	}
	h.mu.Unlock()
	if g == nil || base == "" {
		return servers
	}
	out := make([]mcp.Server, 0, len(servers))
	for _, s := range servers {
		if s.Transport != "stdio" {
			out = append(out, s) // an upstream remote server is passed through untouched
			continue
		}
		out = append(out, mcp.Server{
			Name:      s.Name,
			Transport: "http",
			URL:       g.URLFor(base, s.Name),
			Headers:   map[string]string{"Authorization": "Bearer " + token},
			ProjectID: s.ProjectID,
		})
	}
	return out
}

// mcpGatewayHandle returns the gateway, if MCP is enabled.
func (h *Hub) mcpGatewayHandle() *mcp.Gateway {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mcpGateway
}

// revokeMCPToken invalidates a session's gateway token when the session ends, so a leaked config
// can't outlive the session it was minted for.
func (h *Hub) revokeMCPToken(sessionID string) {
	token := h.mcpTokens.revoke(sessionID)
	if token == "" {
		return
	}
	if g := h.mcpGatewayHandle(); g != nil {
		g.RemoveSessionToken(token)
	}
}

// discoverMCP scans the harnesses' own configuration for servers the registry doesn't have. The
// results are cached so a follow-up import doesn't have to re-read the files (and can't be pointed
// at a server that wasn't actually offered).
func (h *Hub) discoverMCP(cwd string) []mcp.Found {
	r := h.mcpRegistry()
	if r == nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	found := mcp.Discover(home, cwd, r.List())
	h.mu.Lock()
	h.mcpFound = map[string]mcp.Found{}
	for _, f := range found {
		h.mcpFound[f.Server.Name] = f
	}
	h.mu.Unlock()
	return found
}

// importMCP adopts previously-discovered servers by name. Returns how many were added.
func (h *Hub) importMCP(names []string) int {
	r := h.mcpRegistry()
	if r == nil {
		return 0
	}
	h.mu.Lock()
	found := h.mcpFound
	h.mu.Unlock()
	n := 0
	for _, name := range names {
		f, ok := found[name]
		if !ok {
			continue // only ever import something a discover actually offered
		}
		if err := r.Upsert(f.Server); err != nil {
			log.Printf("mcp: import of %q failed: %v", name, err)
			continue
		}
		log.Printf("mcp: imported %q from %s", name, f.Source)
		n++
	}
	return n
}

// SetMCPExclusive records whether the daemon owns MCP for its harnesses.
func (h *Hub) SetMCPExclusive(on bool) {
	h.mu.Lock()
	h.mcpExclusive = on
	h.mu.Unlock()
	log.Printf("mcp: exclusive mode %s — harnesses will %s their own MCP config",
		map[bool]string{true: "ON", false: "off"}[on],
		map[bool]string{true: "IGNORE", false: "also load"}[on])
}

func (h *Hub) mcpExclusiveEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mcpExclusive
}

// discoverCwd picks a project directory to scan for project-level harness config. It uses the most
// recently active session's cwd, which is the project the user is actually looking at.
func (h *Hub) discoverCwd() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var newest *managedSession
	for _, m := range h.sessions {
		if m.meta.cwd == "" {
			continue
		}
		if newest == nil || m.lastActivity.After(newest.lastActivity) {
			newest = m
		}
	}
	if newest == nil {
		return ""
	}
	return newest.meta.cwd
}

// discardMCPToken retracts a token minted for a session that failed to start: out of the gateway's
// accept set and out of the pending map, so a failed create can't leave a live credential behind.
func (h *Hub) discardMCPToken(token string) {
	if token == "" {
		return
	}
	h.mcpTokens.discard(token)
	if g := h.mcpGatewayHandle(); g != nil {
		g.RemoveSessionToken(token)
	}
}
