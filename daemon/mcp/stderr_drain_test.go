package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A server's stderr is the only explanation the user ever gets, and it was read too early.
//
// cmd.Stderr is an io.Writer, not a file, so os/exec copies the pipe on a goroutine of its own and
// that copy is complete only once cmd.Wait() returns. The failure path did not wait for it: readLoop
// closes `closed` the moment STDOUT reaches EOF, and Check then read the stderr buffer immediately.
// The race is biased the wrong way — a server that dies on startup closes both streams at nearly the
// same instant — so the explanation the MCP screen shows would intermittently degrade from
// "boom: missing API key" to a bare "exit status 1".
//
// This test does not rely on winning a race. It makes the child CLOSE STDOUT FIRST and only then
// write its error, which is the same ordering the race produces, every time.
func TestAFailedServerStillExplainsItselfWhenStdoutClosesFirst(t *testing.T) {
	r := NewRegistry(filepath.Join(t.TempDir(), "mcp.json"))
	if err := r.Upsert(Server{
		Name: "broken", Transport: "stdio", Command: "sh",
		// exec 1>&- closes stdout, so the client's read loop sees EOF and gives up on the handshake
		// while the process is still alive and has not yet said why it is failing.
		Args: []string{"-c", `exec 1>&-; sleep 0.2; echo 'boom: missing API key' >&2; exit 1`},
	}); err != nil {
		t.Fatal(err)
	}

	st := r.Check(context.Background(), "broken")
	if st.OK {
		t.Fatal("a server that exits 1 must not report OK")
	}
	if !strings.Contains(st.Error, "missing API key") {
		t.Errorf("error = %q, want it to carry the server's own stderr\n\n"+
			"Without the server's explanation the MCP screen shows a bare exit status, which is the "+
			"difference between a user who can fix their config and one who cannot.", st.Error)
	}
}
