package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errServerClosed = errors.New("lsp: server connection closed")

// server owns a single language-server subprocess and its JSON-RPC connection.
// A dedicated read goroutine (readLoop) demultiplexes responses to callers via the
// pending map and dispatches notifications. All writes are serialized by writeMu.
type server struct {
	langID string
	root   string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex // serializes framed writes to stdin

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	onDiagnostics func(path string, diags []Diagnostic)

	initOnce sync.Once
	initErr  error

	closed chan struct{} // closed when readLoop exits (process gone)
}

// startServer spawns the language-server process rooted at root and starts the
// stderr drain and read goroutines. The initialize handshake is deferred to
// ensureInit so it never runs while holding the Manager lock.
func startServer(command string, args []string, langID, root string, onDiag func(string, []Diagnostic)) (*server, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = root

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &server{
		langID:        langID,
		root:          root,
		cmd:           cmd,
		stdin:         stdin,
		stdout:        bufio.NewReader(stdout),
		pending:       make(map[int64]chan rpcMessage),
		onDiagnostics: onDiag,
		closed:        make(chan struct{}),
	}
	// Drain stderr so the server never blocks on a full pipe; discard the content.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	go s.readLoop()
	return s, nil
}

// readLoop reads framed messages until the process exits, routing responses to
// waiting callers and handling server-initiated requests and notifications.
func (s *server) readLoop() {
	defer close(s.closed)
	for {
		body, err := readFrame(s.stdout)
		if err != nil {
			return // EOF or broken pipe: process is gone
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue // skip garbage rather than tear down the connection
		}
		switch {
		case msg.Method != "" && msg.ID != nil:
			s.handleServerRequest(&msg)
		case msg.Method != "":
			s.handleNotification(&msg)
		case msg.ID != nil:
			s.dispatchResponse(&msg)
		}
	}
}

func (s *server) dispatchResponse(msg *rpcMessage) {
	var id int64
	if err := json.Unmarshal(*msg.ID, &id); err != nil {
		return
	}
	s.pendingMu.Lock()
	ch := s.pending[id]
	s.pendingMu.Unlock()
	if ch != nil {
		// Buffered channel of size 1; a timed-out caller has already been removed,
		// so a non-blocking send avoids leaking a goroutine here.
		select {
		case ch <- *msg:
		default:
		}
	}
}

// handleServerRequest replies to the small set of server->client requests that
// otherwise stall some servers, with an empty (null) success. Everything else is
// ignored — we don't implement optional client-side features.
func (s *server) handleServerRequest(msg *rpcMessage) {
	switch msg.Method {
	case "window/workDoneProgress/create", "client/registerCapability":
		_ = s.write(outReply{JSONRPC: "2.0", ID: *msg.ID, Result: nil})
	}
}

func (s *server) handleNotification(msg *rpcMessage) {
	if msg.Method != "textDocument/publishDiagnostics" || s.onDiagnostics == nil {
		return
	}
	var p publishDiagnosticsParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return
	}
	diags := make([]Diagnostic, 0, len(p.Diagnostics))
	for _, d := range p.Diagnostics {
		diags = append(diags, Diagnostic{
			StartLine: d.Range.Start.Line,
			StartChar: d.Range.Start.Character,
			EndLine:   d.Range.End.Line,
			EndChar:   d.Range.End.Character,
			Severity:  d.Severity,
			Message:   d.Message,
			Source:    d.Source,
		})
	}
	s.onDiagnostics(uriToPath(p.URI), diags)
}

// call sends a request and blocks until the matching response arrives, ctx is
// cancelled, or the server dies. It returns the raw Result on success.
func (s *server) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan rpcMessage, 1)

	s.pendingMu.Lock()
	s.pending[id] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()

	if err := s.write(outgoing{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errServerClosed
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("lsp %s: %w", method, resp.Error)
		}
		return resp.Result, nil
	}
}

// notify sends a notification (no response expected).
func (s *server) notify(method string, params interface{}) error {
	return s.write(outgoing{JSONRPC: "2.0", Method: method, Params: params})
}

// write marshals v and emits it as one framed message under the write lock.
func (s *server) write(v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeFrame(s.stdin, body)
}

// ensureInit performs the initialize/initialized handshake exactly once. It uses a
// generous 20s deadline because gopls and rust-analyzer are slow to start.
func (s *server) ensureInit(context.Context) error {
	s.initOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := s.call(ctx, "initialize", initializeParams(s.root)); err != nil {
			s.initErr = err
			return
		}
		s.initErr = s.notify("initialized", struct{}{})
	})
	return s.initErr
}

// stop performs a graceful LSP shutdown (shutdown request + exit notification),
// then waits briefly and force-kills the process if it hasn't exited.
func (s *server) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.call(ctx, "shutdown", nil)
	_ = s.notify("exit", nil)

	done := make(chan struct{})
	go func() { _, _ = s.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		_ = s.cmd.Process.Kill()
	}
	_ = s.stdin.Close()
}

// initializeParams builds a minimal initialize payload that still advertises the
// capabilities we rely on: full-document sync, publishDiagnostics, hover, and
// definition (with link support).
func initializeParams(root string) map[string]interface{} {
	return map[string]interface{}{
		"processId": os.Getpid(),
		"rootUri":   pathToURI(root),
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"synchronization": map[string]interface{}{
					"dynamicRegistration": false,
					"didSave":             false,
				},
				"publishDiagnostics": map[string]interface{}{
					"relatedInformation": true,
				},
				"hover": map[string]interface{}{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"definition": map[string]interface{}{
					"linkSupport": true,
				},
				"completion": map[string]interface{}{
					"completionItem": map[string]interface{}{
						"snippetSupport": false,
					},
				},
				"formatting": map[string]interface{}{
					"dynamicRegistration": false,
				},
			},
		},
	}
}

// completionItems parses a textDocument/completion result — either a CompletionItem[] or a
// CompletionList {items:[...]} — into a capped, plain list. insertText/textEdit.newText is
// preferred as the inserted text, falling back to the label.
func completionItems(raw json.RawMessage) []CompletionItem {
	if isJSONNull(raw) {
		return nil
	}
	type rawItem struct {
		Label      string          `json:"label"`
		InsertText string          `json:"insertText"`
		TextEdit   json.RawMessage `json:"textEdit"`
		Detail     string          `json:"detail"`
		Kind       int             `json:"kind"`
	}
	var items []rawItem
	var wrap struct {
		Items []rawItem `json:"items"`
	}
	if json.Unmarshal(raw, &wrap) == nil && wrap.Items != nil {
		items = wrap.Items
	} else {
		_ = json.Unmarshal(raw, &items)
	}
	const maxItems = 200
	out := make([]CompletionItem, 0, len(items))
	for _, it := range items {
		if it.Label == "" {
			continue
		}
		insert := it.InsertText
		if len(it.TextEdit) > 0 {
			var te struct {
				NewText string `json:"newText"`
			}
			if json.Unmarshal(it.TextEdit, &te) == nil && te.NewText != "" {
				insert = te.NewText
			}
		}
		if insert == "" {
			insert = it.Label
		}
		out = append(out, CompletionItem{Label: it.Label, Insert: insert, Detail: it.Detail, Kind: it.Kind})
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

// hoverText extracts plain text from a textDocument/hover result. The result is
// {contents, range?}; contents is delegated to extractHoverContents.
func hoverText(result json.RawMessage) string {
	if isJSONNull(result) {
		return ""
	}
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(result, &h); err != nil {
		return ""
	}
	return extractHoverContents(h.Contents)
}

// extractHoverContents normalizes the three legal hover-content shapes into plain
// text: a bare string (MarkedString), a {value} object (MarkupContent /
// {language,value}), or an array of either (MarkedString[]).
func extractHoverContents(raw json.RawMessage) string {
	if isJSONNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Value != "" {
		return obj.Value
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		parts := make([]string, 0, len(arr))
		for _, el := range arr {
			if v := extractHoverContents(el); v != "" {
				parts = append(parts, v)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// extractLocation pulls the first definition target from a textDocument/definition
// result, which may be a Location, Location[], or LocationLink[]. ok=false when the
// result is null/empty or contains no usable target.
func extractLocation(raw json.RawMessage) (Location, bool) {
	if isJSONNull(raw) {
		return Location{}, false
	}
	// Location[] / LocationLink[]: recurse into the first usable element.
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		for _, el := range arr {
			if loc, ok := extractLocation(el); ok {
				return loc, true
			}
		}
		return Location{}, false
	}
	var l struct {
		URI         string   `json:"uri"`         // Location
		Range       rangeObj `json:"range"`       // Location
		TargetURI   string   `json:"targetUri"`   // LocationLink
		TargetRange rangeObj `json:"targetRange"` // LocationLink
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return Location{}, false
	}
	switch {
	case l.URI != "":
		return Location{Path: uriToPath(l.URI), StartLine: l.Range.Start.Line, StartChar: l.Range.Start.Character}, true
	case l.TargetURI != "":
		return Location{Path: uriToPath(l.TargetURI), StartLine: l.TargetRange.Start.Line, StartChar: l.TargetRange.Start.Character}, true
	}
	return Location{}, false
}

// isJSONNull reports whether raw is empty or the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}
