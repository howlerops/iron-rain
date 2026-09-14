package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A restarted server must be told about the open documents AFTER the initialize handshake.
//
// The re-announcement went out from getOrStartServer directly, and the handshake happens later, in
// Open's call to ensureInit. A language server discards every message that arrives before
// `initialize` — that is in the protocol, not a quirk — so the documents were announced to a server
// that was not listening yet, and nothing ever sent them again. Previously-open tabs answered
// nothing for the life of the daemon while a newly opened file worked perfectly, which is what made
// it look like LSP had recovered.
//
// It was silent in both directions: s.notify only writes into a pipe, so it returns nil whatever the
// server does with the bytes, and the existing restart test asserts only the Manager's map
// bookkeeping — rebinding, not delivery. This one reads what actually went down the wire.
func TestRestartedServerIsInitializedBeforeDocumentsAreAnnounced(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "rx.log")
	script := filepath.Join(dir, "fake-lsp.sh")

	// A language server that answers the initialize request and then records everything it is sent.
	//
	// The recorder runs in the FOREGROUND. A background job in a non-interactive shell has its stdin
	// assigned to /dev/null before any redirection it does itself (POSIX), so `cat >> log &` records
	// an empty file however long you wait for it. The canned response goes out first instead; the
	// client matches it by id whenever its own request arrives.
	body := `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`
	src := fmt.Sprintf("#!/bin/sh\nprintf 'Content-Length: %d\\r\\n\\r\\n%s'\ncat >> %q\n",
		len(body), body, log)
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(nil)
	root := t.TempDir()
	dead := &server{langID: "go", root: root, closed: make(chan struct{})}
	m.mu.Lock()
	m.servers[root+"\x00go"] = dead
	m.docs[root+"/a.go"] = &doc{srv: dead, langID: "go", version: 1, uri: "file:///proj/a.go", text: "package a"}
	m.mu.Unlock()
	close(dead.closed) // it crashed

	fresh, err := m.getOrStartServer(root, "go", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.kill() })

	// Wait for the document to be announced at all.
	deadline := time.Now().Add(15 * time.Second)
	var seen string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(log)
		seen = string(b)
		if strings.Contains(seen, "textDocument/didOpen") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	didOpen := strings.Index(seen, "textDocument/didOpen")
	if didOpen < 0 {
		t.Fatalf("the restarted server was never told about the open document at all.\nReceived: %q", seen)
	}
	initialize := strings.Index(seen, `"initialize"`)
	if initialize < 0 {
		t.Fatalf("the document was announced to a server that was never initialized. A language server "+
			"discards everything before the handshake, so that tab answers nothing forever while a "+
			"newly opened file works.\nReceived: %q", seen)
	}
	if initialize > didOpen {
		t.Errorf("didOpen (offset %d) was sent BEFORE initialize (offset %d) — the server drops it.",
			didOpen, initialize)
	}
}
