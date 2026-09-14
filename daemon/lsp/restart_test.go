package lsp

import (
	"bufio"
	"strings"
	"testing"
)

// A crashed language server left every already-open document bound to the corpse.
//
// getOrStartServer evicts a closed server and respawns — that half was already right. But
// m.docs[path].srv still pointed at the dead one, and Open was the only code that ever rewrote that
// field. So hover, definition, completion and symbols on an already-open tab kept calling a dead
// connection and failing for the life of the daemon, while opening a THIRD file spawned a healthy
// server and made LSP look recovered. The user's only escape was closing and re-opening each tab —
// and CodeSurface early-returns for a tab that is already open, so even that did not work.
//
// Rebinding alone is not enough either: a replacement process has never been told about those
// documents, and a language server only answers for documents it received a didOpen for. That is why
// the doc now keeps its text.
func TestDocumentsAreReboundAndReannouncedWhenAServerRestarts(t *testing.T) {
	m := NewManager(nil)
	// A REAL directory: startServer sets cmd.Dir to the root, so a nonexistent one fails the exec
	// for a reason that has nothing to do with what is being tested here.
	root := t.TempDir()

	// Two documents on one server, the two-tab case from the report.
	srv := &server{langID: "go", root: root, closed: make(chan struct{})}
	m.mu.Lock()
	m.servers[""+root+"\x00go"] = srv
	m.docs[root+"/a.go"] = &doc{srv: srv, langID: "go", version: 1, uri: "file:///proj/a.go", text: "package a"}
	m.docs[root+"/b.go"] = &doc{srv: srv, langID: "go", version: 1, uri: "file:///proj/b.go", text: "package b"}
	m.mu.Unlock()

	close(srv.closed) // the server crashes

	// Spawning the replacement is what triggers the rebind. `true` is a real binary that exits
	// immediately, which is all this needs — the assertions are about the Manager's bookkeeping.
	fresh, err := m.getOrStartServer(root, "go", "/bin/sh", []string{"-c", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh == srv {
		t.Fatal("the dead server was reused")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, path := range []string{root + "/a.go", root + "/b.go"} {
		d, ok := m.docs[path]
		if !ok {
			t.Errorf("%s was dropped rather than rebound — hover on that tab now silently answers "+
				"nothing, which looks identical to a file with no symbols", path)
			continue
		}
		if d.srv == srv {
			t.Errorf("%s is still bound to the CRASHED server. Every editor request on that tab will "+
				"fail with \"server connection closed\" forever, while a newly opened file works fine "+
				"— so LSP looks recovered when two tabs are permanently dead.", path)
		}
		if d.text == "" {
			t.Errorf("%s kept no content, so the restarted server can never be told about it and will "+
				"answer nothing for it however it is bound", path)
		}
	}
}

// A frame header is attacker- or corruption-controlled and went straight to make() with no ceiling.
// "Content-Length: 9000000000000000000" panics the allocation on a goroutine with no recover(),
// which kills oculusd outright: every agent session drops and every client disconnects.
func TestAnAbsurdContentLengthIsRefusedRatherThanAllocated(t *testing.T) {
	r := strings.NewReader("Content-Length: 9000000000000000000\r\n\r\n")
	if _, err := readFrame(bufio.NewReader(r)); err == nil {
		t.Error("a 9-exabyte frame was accepted — make() panics on this, on a goroutine with no " +
			"recover, taking the whole daemon with it")
	}

	// A merely large-but-wrong value allocates and then blocks in ReadFull forever, holding it.
	r2 := strings.NewReader("Content-Length: 2147483648\r\n\r\n")
	if _, err := readFrame(bufio.NewReader(r2)); err == nil {
		t.Error("a 2 GiB frame was accepted — the daemon allocates it and then waits forever for " +
			"bytes that are not coming, with the memory held")
	}

	// An ordinary frame must still work.
	r3 := strings.NewReader("Content-Length: 2\r\n\r\n{}")
	body, err := readFrame(bufio.NewReader(r3))
	if err != nil || string(body) != "{}" {
		t.Errorf("an ordinary frame was refused: %q, %v", body, err)
	}
}
