package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Server is one registered MCP server definition.
//
// Two transports: "stdio" (a local command the daemon and each harness spawn) and "http" (a remote
// streamable-HTTP endpoint). stdio is what almost every published server uses today.
type Server struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`         // stdio | http
	Command   string            `json:"command,omitempty"` // stdio
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"` // may hold credentials — file is 0600
	Cwd       string            `json:"cwd,omitempty"`
	URL       string            `json:"url,omitempty"`     // http
	Headers   map[string]string `json:"headers,omitempty"` // http auth
	Disabled  bool              `json:"disabled,omitempty"`
	ProjectID string            `json:"project_id,omitempty"` // "" = available to every project
}

// Status is the last thing we learned about a server by actually talking to it.
type Status struct {
	Name            string    `json:"name"`
	OK              bool      `json:"ok"`
	Error           string    `json:"error,omitempty"`
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	ServerVersion   string    `json:"server_version,omitempty"`
	Tools           []Tool    `json:"tools,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
}

// Registry is the persisted set of MCP servers plus their last probe results.
//
// Persistence follows the loops-engine shape rather than the agents.json one: a single mutex is held
// across the whole load→mutate→save cycle, and the snapshot is written OUTSIDE the lock. agents.json
// does an unlocked read-modify-write from concurrent handlers and can silently lose an edit; this
// must not inherit that bug.
type Registry struct {
	mu      sync.Mutex
	path    string
	servers []Server
	status  map[string]Status
}

// persisted is the on-disk shape. A wrapper object (not a bare array) so fields can be added later
// without another migration.
type persisted struct {
	Servers []Server `json:"servers"`
}

// NewRegistry loads the registry at path. A missing file is not an error.
func NewRegistry(path string) *Registry {
	r := &Registry{path: path, status: map[string]Status{}}
	if data, err := os.ReadFile(path); err == nil {
		var p persisted
		if json.Unmarshal(data, &p) == nil {
			r.servers = p.Servers
		}
	}
	return r
}

// List returns the registered servers, sorted by name.
func (r *Registry) List() []Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Server, len(r.servers))
	copy(out, r.servers)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Status returns the last probe result for each server.
func (r *Registry) Statuses() map[string]Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Status, len(r.status))
	for k, v := range r.status {
		out[k] = v
	}
	return out
}

// Get returns one server by name.
func (r *Registry) Get(name string) (Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.servers {
		if s.Name == name {
			return s, true
		}
	}
	return Server{}, false
}

// Upsert adds or replaces a server by name and persists.
func (r *Registry) Upsert(s Server) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Transport == "" {
		s.Transport = "stdio"
	}
	r.mu.Lock()
	replaced := false
	for i := range r.servers {
		if r.servers[i].Name == s.Name {
			r.servers[i] = s
			replaced = true
			break
		}
	}
	if !replaced {
		r.servers = append(r.servers, s)
	}
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	return r.write(snapshot)
}

// Delete removes a server by name and persists.
func (r *Registry) Delete(name string) error {
	r.mu.Lock()
	kept := r.servers[:0]
	for _, s := range r.servers {
		if s.Name != name {
			kept = append(kept, s)
		}
	}
	r.servers = kept
	delete(r.status, name)
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	return r.write(snapshot)
}

// SetEnabled toggles a server and persists.
func (r *Registry) SetEnabled(name string, enabled bool) error {
	r.mu.Lock()
	found := false
	for i := range r.servers {
		if r.servers[i].Name == name {
			r.servers[i].Disabled = !enabled
			found = true
			break
		}
	}
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	if !found {
		return os.ErrNotExist
	}
	return r.write(snapshot)
}

func (r *Registry) snapshotLocked() persisted {
	out := persisted{Servers: make([]Server, len(r.servers))}
	copy(out.Servers, r.servers)
	return out
}

// write persists a snapshot. Called with the lock RELEASED — disk I/O never blocks a reader.
func (r *Registry) write(p persisted) error {
	if r.path == "" {
		return nil
	}
	if dir := filepath.Dir(r.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	// 0600: Env/Headers routinely hold API keys.
	return os.WriteFile(r.path, data, 0o600)
}

// Check connects to a server, probes it, records the result, and disconnects.
//
// It deliberately does NOT hold the connection open. The harnesses spawn their own clients from the
// same definition, so a permanently-connected daemon copy would double every server process for no
// benefit. The daemon connects to answer one question — does this work, and what does it offer —
// then gets out of the way.
func (r *Registry) Check(ctx context.Context, name string) Status {
	s, ok := r.Get(name)
	if !ok {
		return Status{Name: name, Error: "no such server"}
	}
	st := Status{Name: name, CheckedAt: time.Now()}
	if s.Transport == "http" {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		info, err := DialRemote(s.URL, s.Headers).Probe(ctx)
		if err != nil {
			st.Error = err.Error()
		} else {
			st.OK = true
			st.ProtocolVersion, st.ServerVersion, st.Tools = info.ProtocolVersion, info.Version, info.Tools
		}
		r.record(st)
		return st
	}
	if strings.TrimSpace(s.Command) == "" {
		st.Error = "no command configured"
		r.record(st)
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	c, err := Dial(ctx, s.Command, s.Args, s.Env, s.Cwd)
	if err != nil {
		st.Error = err.Error()
		r.record(st)
		return st
	}
	defer c.Close()
	info, err := c.Probe(ctx)
	if err != nil {
		st.Error = err.Error()
		// Compare against the LINE that would be appended, not the whole multi-line stderr. Client.call
		// already appends that last line, and a multi-line tail never matches Contains — so the same
		// line was appended a second time and the reason appeared twice in one message.
		if tail := strings.TrimSpace(c.Stderr()); tail != "" {
			if line := lastLine(tail); line != "" && !strings.Contains(st.Error, line) {
				st.Error += " — " + line
			}
		}
		r.record(st)
		return st
	}
	st.OK = true
	st.ProtocolVersion = info.ProtocolVersion
	st.ServerVersion = info.Version
	st.Tools = info.Tools
	r.record(st)
	return st
}

func (r *Registry) record(st Status) {
	r.mu.Lock()
	r.status[st.Name] = st
	r.mu.Unlock()
}

// Enabled returns the servers that should be injected into a harness for a given project. A server
// scoped to another project is excluded; an unscoped one applies everywhere.
func (r *Registry) Enabled(projectID string) []Server {
	var out []Server
	for _, s := range r.List() {
		if s.Disabled {
			continue
		}
		if s.ProjectID != "" && s.ProjectID != projectID {
			continue
		}
		out = append(out, s)
	}
	return out
}
