package mcp

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// Manager keeps ONE long-lived connection per MCP server, shared by every harness.
//
// This is the difference between the daemon being an MCP *host* and being a config manager. Without
// it, each harness spawns its own copy of every server: three agents with the same GitHub server
// means three GitHub server processes, three sets of credentials in flight, three rate-limit
// budgets, and no single place where a tool call can be seen or audited. With it, the server runs
// once and the daemon is the only thing that talks to it.
//
// Supervision is what the LSP manager never had (see the roadmap's §0.6): a dead connection is
// evicted and respawned on the next use, with backoff so a server that crashes on startup can't
// become a spawn loop.

const (
	// restartBackoffMin is the first delay after a crash; it doubles up to restartBackoffMax.
	restartBackoffMin = 500 * time.Millisecond
	restartBackoffMax = 30 * time.Second
	// healthyFor is how long a connection must survive before its backoff resets. A server that runs
	// fine for a while and then dies is a fresh incident, not a continuing failure.
	healthyFor = 60 * time.Second
)

// Manager supervises live server connections.
type Manager struct {
	mu    sync.Mutex
	conns map[string]*supervised
	reg   *Registry
	// remoteVersions caches the negotiated protocol for hosted servers, which have no persistent
	// connection to hang it off.
	remoteVersions map[string]string
}

// supervised is one server's connection plus its restart state.
type supervised struct {
	client    *Client
	startedAt time.Time
	backoff   time.Duration
	nextTry   time.Time
	protocol  string
}

// NewManager returns a manager backed by reg.
func NewManager(reg *Registry) *Manager {
	return &Manager{conns: map[string]*supervised{}, reg: reg, remoteVersions: map[string]string{}}
}

// Caller is what the gateway proxies through: either a supervised stdio process or a hosted HTTP
// server. Both answer one JSON-RPC request at a time, which is all the gateway needs.
type Caller interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// Dial returns something to proxy through for the named server, plus its protocol version. Remote
// servers need no supervision — there is no process to watch — so they bypass the restart machinery
// entirely and are negotiated per call cycle.
func (m *Manager) Dial(ctx context.Context, name string) (Caller, string, error) {
	srv, ok := m.reg.Get(name)
	if !ok {
		return nil, "", &NotFoundError{Server: name}
	}
	if srv.Disabled {
		return nil, "", &DisabledError{Server: name}
	}
	if srv.Transport == "http" {
		rc := DialRemote(srv.URL, srv.Headers)
		version := m.remoteVersion(name)
		if version == "" {
			info, err := rc.negotiate(ctx)
			if err != nil {
				return nil, "", err
			}
			version = info.ProtocolVersion
			m.rememberRemoteVersion(name, version)
		}
		return rc, version, nil
	}
	return m.Get(ctx, name)
}

// remoteVersion returns a cached negotiated version for a hosted server ("" = not yet known).
func (m *Manager) remoteVersion(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remoteVersions[name]
}

func (m *Manager) rememberRemoteVersion(name, version string) {
	m.mu.Lock()
	if m.remoteVersions == nil {
		m.remoteVersions = map[string]string{}
	}
	m.remoteVersions[name] = version
	m.mu.Unlock()
}

// Get returns a live, negotiated connection to the named server, starting or restarting it as
// needed. The returned protocol version tells the caller which request shape to use.
func (m *Manager) Get(ctx context.Context, name string) (*Client, string, error) {
	m.mu.Lock()
	s := m.conns[name]
	if s != nil {
		if !s.client.isClosed() {
			c, p := s.client, s.protocol
			m.mu.Unlock()
			return c, p, nil
		}
		// The connection died. Reset backoff if it had been healthy for a while — a long-running
		// server that finally crashed is a new incident, not a continuing failure.
		if time.Since(s.startedAt) > healthyFor {
			s.backoff = 0
		}
		if s.backoff == 0 {
			s.backoff = restartBackoffMin
		} else if s.backoff < restartBackoffMax {
			s.backoff *= 2
		}
		if time.Now().Before(s.nextTry) {
			wait := time.Until(s.nextTry)
			m.mu.Unlock()
			return nil, "", &BackoffError{Server: name, RetryIn: wait}
		}
		s.nextTry = time.Now().Add(s.backoff)
		log.Printf("mcp: %s died — restarting (backoff now %s)", name, s.backoff)
	}
	m.mu.Unlock()

	srv, ok := m.reg.Get(name)
	if !ok {
		return nil, "", &NotFoundError{Server: name}
	}
	if srv.Disabled {
		return nil, "", &DisabledError{Server: name}
	}

	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	c, err := Dial(dialCtx, srv.Command, srv.Args, srv.Env, srv.Cwd)
	if err != nil {
		return nil, "", err
	}
	info, err := c.negotiate(dialCtx)
	if err != nil {
		c.Close()
		return nil, "", err
	}

	m.mu.Lock()
	// Another goroutine may have connected while we were dialing; keep whichever is live and close
	// the loser so we never leak a server process.
	if cur := m.conns[name]; cur != nil && !cur.client.isClosed() {
		m.mu.Unlock()
		c.Close()
		return cur.client, cur.protocol, nil
	}
	prev := m.conns[name]
	next := &supervised{client: c, startedAt: time.Now(), protocol: info.ProtocolVersion}
	if prev != nil {
		next.backoff, next.nextTry = prev.backoff, prev.nextTry
	}
	m.conns[name] = next
	m.mu.Unlock()
	// Deliberately does NOT report a tool count: negotiate only establishes the protocol, and listing
	// tools here would cost an extra round trip on every reconnect for a number nothing consumes.
	log.Printf("mcp: %s connected (protocol %s)", name, info.ProtocolVersion)
	return c, info.ProtocolVersion, nil
}

// Close shuts down one server's connection (e.g. after it is disabled or edited).
func (m *Manager) Close(name string) {
	m.mu.Lock()
	s := m.conns[name]
	delete(m.conns, name)
	m.mu.Unlock()
	if s != nil {
		s.client.Close()
	}
}

// Shutdown closes every supervised connection. Called on daemon shutdown — which, since signal
// handling landed, actually runs.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	conns := make([]*supervised, 0, len(m.conns))
	for _, s := range m.conns {
		conns = append(conns, s)
	}
	m.conns = map[string]*supervised{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range conns {
		wg.Add(1)
		go func(s *supervised) {
			defer wg.Done()
			s.client.Close()
		}(s)
	}
	wg.Wait()
}

// isClosed reports whether the client's read loop has exited.
func (c *Client) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// BackoffError means the server crashed recently and we're waiting before retrying.
type BackoffError struct {
	Server  string
	RetryIn time.Duration
}

func (e *BackoffError) Error() string {
	return "mcp: " + e.Server + " is restarting; retry in " + e.RetryIn.Round(time.Millisecond).String()
}

// NotFoundError means no such server is registered.
type NotFoundError struct{ Server string }

func (e *NotFoundError) Error() string { return "mcp: no server named " + e.Server }

// DisabledError means the server exists but is turned off.
type DisabledError struct{ Server string }

func (e *DisabledError) Error() string { return "mcp: server " + e.Server + " is disabled" }
