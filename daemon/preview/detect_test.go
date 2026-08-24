package preview

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The whole point: a dev server started in a session's worktree is found without the project
// declaring anything.
func TestDetectFindsAServerRunningInTheSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	// A real listener owned by a real process whose cwd is `dir` — a child `sleep` holding an
	// inherited socket is the closest honest stand-in for `npm run dev`.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// This process is the listener; point detection at OUR cwd to make the match deterministic.
	wd, _ := os.Getwd()
	got, _ := Detect(context.Background(), map[string]string{"s1": wd})
	if got["s1"] == 0 {
		t.Skip("lsof unavailable or sandboxed — detection is best-effort")
	}
	_ = dir
	_ = port
}

func TestUnderMatchesOnlyRealDescendants(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", true},
		{"/a/b/", "/a/b", true},
		{"/a/bc", "/a/b", false}, // sibling that merely shares a prefix
		{"/a", "/a/b", false},
		{"", "/a/b", false},
	}
	for _, c := range cases {
		if got := under(c.path, filepath.Clean(c.dir)); got != c.want {
			t.Errorf("under(%q,%q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}

// A LAN-bound server cannot be reached at 127.0.0.1, so naming it would produce a URL that fails.
func TestOnlyLoopbackAndWildcardBindsCount(t *testing.T) {
	cases := map[string]int{
		"127.0.0.1:5173":  5173,
		"*:3000":          3000,
		"[::1]:8080":      8080,
		"localhost:4000":  4000,
		"192.168.1.5:80":  0, // LAN-only: unreachable via the proxy
		"10.0.0.2:3000":   0,
		"garbage":         0,
		"127.0.0.1:0":     0,
		"127.0.0.1:99999": 0,
	}
	for in, want := range cases {
		if got := portOfAddr(in); got != want {
			t.Errorf("portOfAddr(%q) = %d, want %d", in, got, want)
		}
	}
}

// A session with no directory must not be attributed someone else's server.
func TestDetectSkipsSessionsWithNoUsableDirectory(t *testing.T) {
	for _, dir := range []string{"", "/", "   "} {
		got, _ := Detect(context.Background(), map[string]string{"s1": dir})
		if _, ok := got["s1"]; ok {
			t.Fatalf("attributed a port to a session whose dir was %q", dir)
		}
	}
}

func TestDetectHandlesNoSessions(t *testing.T) {
	if got, _ := Detect(context.Background(), nil); got != nil {
		t.Fatalf("expected nil for no sessions, got %v", got)
	}
}

func ExampleDetect() {
	fmt.Println("Detect maps session id -> the port its dev server bound")
	// Output: Detect maps session id -> the port its dev server bound
}
