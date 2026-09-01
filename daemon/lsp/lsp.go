// Package lsp manages language-server subprocesses and speaks the Language Server
// Protocol (JSON-RPC 2.0 over stdio) so the built-in code editor can obtain
// diagnostics, hover type information, and go-to-definition.
//
// A single Manager owns servers keyed by (project root, languageId) and multiplexes
// open documents across them. Everything degrades gracefully: an unsupported file
// type or a missing server binary turns the document operations into no-ops so plain
// editing keeps working.
package lsp

import (
	"context"
	"log"
	"sync"
	"time"
)

// requestTimeout caps synchronous hover/definition requests so a hung server can
// never block the editor for longer than this.
const requestTimeout = 5 * time.Second

// Diagnostic mirrors LSP publishDiagnostics. Positions are 0-based (LSP native).
type Diagnostic struct {
	StartLine, StartChar int
	EndLine, EndChar     int
	Severity             int // 1=Error 2=Warning 3=Info 4=Hint
	Message              string
	Source               string
}

// Location is a go-to-definition target (0-based position).
type Location struct {
	Path      string
	StartLine int
	StartChar int
}

// doc tracks an open document: which server owns it, its language, the current
// sync version, and its file:// URI (cached to avoid recomputing).
type doc struct {
	srv     *server
	langID  string
	version int
	uri     string
}

// Manager owns language-server subprocesses keyed by (rootDir, languageID) and
// multiplexes documents across them. Safe for concurrent use.
//
// The Manager-level lock (mu) guards only the server and doc maps; it is never held
// across server I/O, so a slow or hung server cannot stall unrelated operations.
type Manager struct {
	onDiagnostics func(path string, diags []Diagnostic)

	mu      sync.Mutex
	servers map[string]*server // key: rootDir + "\x00" + languageID
	docs    map[string]*doc    // key: absolute file path
}

// NewManager returns a Manager. onDiagnostics is invoked (from an internal
// goroutine — the caller must be thread-safe) whenever a server publishes
// diagnostics for a file; path is the absolute file path (converted back from the
// LSP file:// URI).
func NewManager(onDiagnostics func(path string, diags []Diagnostic)) *Manager {
	return &Manager{
		onDiagnostics: onDiagnostics,
		servers:       make(map[string]*server),
		docs:          make(map[string]*doc),
	}
}

// getOrStartServer returns the existing server for (root, langID) or spawns a new
// one. Spawning is cheap (exec.Start); the expensive initialize handshake happens
// later in ensureInit, outside the lock.
func (m *Manager) getOrStartServer(root, langID, command string, args []string) (*server, error) {
	key := root + "\x00" + langID

	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[key]; ok {
		// A crashed server used to stay in this map forever: readLoop returns, closed is closed, and
		// every later call failed errServerClosed for the life of the daemon — LSP silently died until
		// restart. Evict the corpse and fall through to a fresh spawn.
		if !s.isClosed() {
			return s, nil
		}
		log.Printf("lsp: %s server for %s exited — restarting", langID, root)
		delete(m.servers, key)
	}
	s, err := startServer(command, args, langID, root, m.onDiagnostics)
	if err != nil {
		return nil, err
	}
	m.servers[key] = s
	return s, nil
}

// Open ensures a server is running for the file's language rooted at the file's
// project root, performs the initialize handshake if needed, and sends
// textDocument/didOpen. No-op returning nil if the language is unsupported or its
// server binary isn't installed (graceful degrade — editing still works, just no LSP).
func (m *Manager) Open(ctx context.Context, path, content string) error {
	langID := languageID(path)
	if langID == "" {
		return nil
	}
	command, args, ok := serverCommand(langID)
	if !ok {
		return nil
	}

	srv, err := m.getOrStartServer(findRoot(path, langID), langID, command, args)
	if err != nil {
		return err
	}
	// Handshake outside the Manager lock — this can take many seconds.
	if err := srv.ensureInit(ctx); err != nil {
		return err
	}

	uri := pathToURI(path)
	m.mu.Lock()
	d, exists := m.docs[path]
	if !exists {
		d = &doc{srv: srv, langID: langID, version: 1, uri: uri}
		m.docs[path] = d
	} else {
		// Re-open of an already tracked file: bump version and rebind the server.
		d.version++
		d.srv = srv
		d.uri = uri
	}
	ver := d.version
	m.mu.Unlock()

	return srv.notify("textDocument/didOpen", didOpenParams{
		TextDocument: textDocumentItem{URI: uri, LanguageID: langID, Version: ver, Text: content},
	})
}

// Change sends textDocument/didChange (full-document sync, incrementing version).
// No-op if the file was never Opened / has no server.
func (m *Manager) Change(_ context.Context, path, content string) error {
	m.mu.Lock()
	d, ok := m.docs[path]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	d.version++
	ver, srv, uri := d.version, d.srv, d.uri
	m.mu.Unlock()

	return srv.notify("textDocument/didChange", didChangeParams{
		TextDocument:   versionedTextDocumentIdentifier{URI: uri, Version: ver},
		ContentChanges: []contentChange{{Text: content}},
	})
}

// Close sends textDocument/didClose for the file.
func (m *Manager) Close(path string) {
	m.mu.Lock()
	d, ok := m.docs[path]
	if ok {
		delete(m.docs, path)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	_ = d.srv.notify("textDocument/didClose", didCloseParams{
		TextDocument: textDocumentIdentifier{URI: d.uri},
	})
}

// lookupDoc returns the tracked doc for path, if any, without holding the lock
// during the subsequent request.
func (m *Manager) lookupDoc(path string) (*doc, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.docs[path]
	return d, ok
}

// Hover returns the hover text (type info / docs) at a 0-based position, or "" if none.
func (m *Manager) Hover(ctx context.Context, path string, line, char int) (string, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := d.srv.call(ctx, "textDocument/hover", positionParams{
		TextDocument: textDocumentIdentifier{URI: d.uri},
		Position:     position{Line: line, Character: char},
	})
	if err != nil {
		return "", err
	}
	return hoverText(res), nil
}

// Definition returns the definition location for the symbol at a 0-based position.
// A zero Location with a nil error means the server reported no definition.
func (m *Manager) Definition(ctx context.Context, path string, line, char int) (Location, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return Location{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := d.srv.call(ctx, "textDocument/definition", positionParams{
		TextDocument: textDocumentIdentifier{URI: d.uri},
		Position:     position{Line: line, Character: char},
	})
	if err != nil {
		return Location{}, err
	}
	loc, _ := extractLocation(res)
	return loc, nil
}

// CompletionItem is one autocomplete suggestion.
type CompletionItem struct {
	Label  string
	Insert string
	Detail string
	Kind   int
}

// Completion returns autocomplete suggestions at a 0-based position (nil if none / no server).
func (m *Manager) Completion(ctx context.Context, path string, line, char int) ([]CompletionItem, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := d.srv.call(ctx, "textDocument/completion", positionParams{
		TextDocument: textDocumentIdentifier{URI: d.uri},
		Position:     position{Line: line, Character: char},
	})
	if err != nil {
		return nil, err
	}
	return completionItems(res), nil
}

// Shutdown stops all language servers (LSP shutdown + exit, then kill).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for key, s := range m.servers {
		servers = append(servers, s)
		delete(m.servers, key)
	}
	m.docs = make(map[string]*doc)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			s.stop()
		}(s)
	}
	// BOUNDED, for the same reason hub.closeSessions is: shutdown has to finish whatever a child
	// does. A bare wg.Wait() here hung the whole daemon on a language server that would not exit —
	// the process had already stopped listening, so it was unreachable, yet it stayed alive holding
	// the state lock and every restart failed with "another oculusd is already using …". Observed at
	// 90 minutes, cleared only by SIGKILL. The straggler is named rather than hidden: it is a child
	// we are about to leak, and this line is the only evidence anyone gets.
	if !waitBounded(&wg, shutdownBudget) {
		log.Printf("lsp: gave up after %s waiting on %d language server(s) to exit", shutdownBudget, len(servers))
	}
}

// shutdownBudget caps how long teardown may take before the process gives up on a child.
const shutdownBudget = 5 * time.Second

// waitBounded reports whether wg finished within d.
func waitBounded(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// --- LSP protocol parameter / result types (0-based positions) ---

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type rangeObj struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type contentChange struct {
	Text string `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type positionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
}

type publishDiagnosticsParams struct {
	URI         string          `json:"uri"`
	Diagnostics []lspDiagnostic `json:"diagnostics"`
}

type lspDiagnostic struct {
	Range    rangeObj `json:"range"`
	Severity int      `json:"severity"`
	Message  string   `json:"message"`
	Source   string   `json:"source"`
}
