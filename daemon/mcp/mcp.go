// Package mcp is the daemon's Model Context Protocol host: one registry of MCP servers, owned by the
// daemon, shared by every coding agent it drives.
//
// Why the DAEMON owns this rather than each harness: today a user configures the same server three
// times (once in opencode.json, once in .mcp.json, once wherever the next CLI wants it), with three
// copies of the credentials and no single place to see what's installed or whether it works. The
// daemon already supervises every agent, already stores secrets at 0600, and is already reachable
// from the phone — so it is the right owner. Servers are defined once here and injected into each
// harness's own config (see the injection helpers), which means this works with harnesses that know
// nothing about Iron Rain.
//
// Protocol note (2026): MCP revision 2026-07-28 made the protocol stateless — no initialize
// handshake, no sessions, a mandatory server/discover, per-request _meta. The overwhelming majority
// of published servers still speak the 2025-11-25 style, so this client is DUAL-STACK: it probes
// server/discover first and falls back to initialize. See negotiate().
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/howlerops/oculus/daemon/procutil"
)

// Protocol revisions this host understands, newest first.
const (
	ProtocolLatest = "2026-07-28"
	ProtocolLegacy = "2025-11-25"
)

// connectTimeout bounds a whole connect+negotiate+list cycle. Generous because a first `npx -y …`
// run downloads the package before the server even starts.
const connectTimeout = 90 * time.Second

var errClosed = errors.New("mcp: server connection closed")

// Tool is one tool a server advertises.
type Tool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// ServerInfo is what a probe learned about a server.
type ServerInfo struct {
	Name            string
	Version         string
	ProtocolVersion string
	Tools           []Tool
}

// Client is a live stdio JSON-RPC connection to one MCP server process.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	closeOnce sync.Once
	closed    chan struct{}

	stderr *lineBuffer // recent stderr, so a failed connect can say WHY
}

// Dial starts a stdio MCP server and returns a connected client. The caller must Close it.
func Dial(ctx context.Context, command string, args []string, env map[string]string, cwd string) (*Client, error) {
	cmd := exec.Command(command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = mergedEnv(env)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Capture stderr rather than discarding it: when a server fails to start, its stderr is the only
	// explanation the user will ever get ("MODULE_NOT_FOUND", "missing API key"). Bounded so a chatty
	// server can't grow memory, and always drained so it can't block on a full pipe.
	lb := newLineBuffer(40)
	cmd.Stderr = lb
	procutil.Isolate(cmd) // `npx foo` is a wrapper that forks the real server — own the whole tree
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		pending: make(map[int64]chan rpcMessage),
		closed:  make(chan struct{}),
		stderr:  lb,
	}
	go c.readLoop()
	go func() {
		_ = cmd.Wait() // reap; without this the exited server lingers as a zombie
	}()
	return c, nil
}

// mergedEnv returns the process environment with the given overrides applied.
func mergedEnv(env map[string]string) []string {
	base := osEnviron()
	for k, v := range env {
		base = append(base, k+"="+v)
	}
	return base
}

// Close terminates the server's whole process group and unblocks any waiting caller.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		procutil.TerminateGroup(c.cmd)
	})
	return nil
}

// Stderr returns the server's recent stderr — the explanation for a failed start.
func (c *Client) Stderr() string { return c.stderr.String() }

func (c *Client) readLoop() {
	defer close(c.closed)
	for {
		body, err := readFrame(c.stdout)
		if err != nil {
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue // skip garbage rather than tear down the connection
		}
		if msg.ID == nil {
			continue // notification: nothing we act on yet
		}
		var id int64
		if json.Unmarshal(*msg.ID, &id) != nil {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[id]
		c.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

// call sends a request and waits for its response.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.write(outgoing{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		if s := strings.TrimSpace(c.Stderr()); s != "" {
			return nil, fmt.Errorf("%w: %s", errClosed, lastLine(s))
		}
		return nil, errClosed
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(outgoing{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.stdin, body)
}

// Probe negotiates with the server and lists its tools — everything the UI needs to show that a
// server works and what it offers.
func (c *Client) Probe(ctx context.Context) (ServerInfo, error) {
	info, err := c.negotiate(ctx)
	if err != nil {
		return ServerInfo{}, err
	}
	tools, err := c.ListTools(ctx, info.ProtocolVersion)
	if err != nil {
		return info, err // negotiation worked; report what we learned plus the failure
	}
	info.Tools = tools
	return info, nil
}

// negotiate establishes the protocol revision. It tries the 2026-07-28 `server/discover` first and
// falls back to the pre-2026 `initialize` handshake, because ~every server published before mid-2026
// only implements the latter and there are far more of those than new ones.
func (c *Client) negotiate(ctx context.Context) (ServerInfo, error) {
	if raw, err := c.call(ctx, "server/discover", map[string]any{
		"_meta": metaFor(ProtocolLatest),
	}); err == nil {
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
	// Legacy handshake.
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
	// The pre-2026 protocol requires this notification before any other request.
	_ = c.notify("notifications/initialized", map[string]any{})
	v := init.ProtocolVersion
	if v == "" {
		v = ProtocolLegacy
	}
	return ServerInfo{Name: init.ServerInfo.Name, Version: init.ServerInfo.Version, ProtocolVersion: v}, nil
}

// ListTools returns the server's tools. protocolVersion decides whether per-request _meta is sent.
func (c *Client) ListTools(ctx context.Context, protocolVersion string) ([]Tool, error) {
	params := map[string]any{}
	if protocolVersion == ProtocolLatest {
		params["_meta"] = metaFor(protocolVersion)
	}
	raw, err := c.call(ctx, "tools/list", params)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// metaFor builds the per-request _meta the 2026-07-28 revision requires on every request.
func metaFor(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "iron-rain", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// --- JSON-RPC framing (Content-Length, same as LSP) ---

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc %d: %s", e.Code, e.Message) }

type outgoing struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func writeFrame(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// maxFrameBytes bounds one message so a malformed or hostile Content-Length can't make the daemon
// allocate unbounded memory.
const maxFrameBytes = 16 << 20

func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "content-length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("mcp: bad Content-Length %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("mcp: message without Content-Length")
	}
	if length > maxFrameBytes {
		return nil, fmt.Errorf("mcp: frame too large (%d bytes)", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// lineBuffer keeps the last N lines written to it. Used for a server's stderr tail.
type lineBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
	part  strings.Builder
}

func newLineBuffer(max int) *lineBuffer { return &lineBuffer{max: max} }

func (b *lineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range string(p) {
		if ch == '\n' {
			b.push(b.part.String())
			b.part.Reset()
			continue
		}
		b.part.WriteRune(ch)
		if b.part.Len() > 4000 { // a single endless line must not grow without bound
			b.push(b.part.String())
			b.part.Reset()
		}
	}
	return len(p), nil
}

func (b *lineBuffer) push(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	b.lines = append(b.lines, line)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

func (b *lineBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := strings.Join(b.lines, "\n")
	if b.part.Len() > 0 {
		if out != "" {
			out += "\n"
		}
		out += b.part.String()
	}
	return out
}

// osEnviron is a seam so tests can render env deterministically.
var osEnviron = os.Environ
