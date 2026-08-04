package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

func TestParseListeners(t *testing.T) {
	out := []byte(`COMMAND     PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
opencode  12345 jacob   14u  IPv4 0x1234567890abcdef      0t0  TCP 127.0.0.1:4096 (LISTEN)
node      99999 jacob   20u  IPv4 0xfedcba0987654321      0t0  TCP 127.0.0.1:3000 (LISTEN)
opencode  12346 jacob   15u  IPv6 0x1111222233334444      0t0  TCP [::1]:65036 (LISTEN)
`)
	ls := ParseListeners(out)
	if len(ls) != 3 {
		t.Fatalf("want 3 listeners, got %d: %+v", len(ls), ls)
	}
	if ls[0].Command != "opencode" || ls[0].PID != 12345 || ls[0].Port != 4096 {
		t.Errorf("listener 0 = %+v", ls[0])
	}
	if ls[2].Port != 65036 || ls[2].PID != 12346 {
		t.Errorf("listener 2 (IPv6) = %+v", ls[2])
	}
}

func TestFindOpenCodeServersFiltersByCommand(t *testing.T) {
	orig := lsof
	lsof = func(_ context.Context) ([]byte, error) {
		return []byte(`COMMAND  PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
opencode 111 jacob 5u IPv4 0x0 0t0 TCP 127.0.0.1:4096 (LISTEN)
node     222 jacob 6u IPv4 0x0 0t0 TCP 127.0.0.1:3000 (LISTEN)
opencode 111 jacob 7u IPv4 0x0 0t0 TCP 127.0.0.1:4096 (LISTEN)
`), nil
	}
	defer func() { lsof = orig }()

	servers, err := FindOpenCodeServers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("want 1 opencode server (deduped), got %d: %+v", len(servers), servers)
	}
	if servers[0].URL != "http://127.0.0.1:4096" || servers[0].PID != 111 {
		t.Errorf("server = %+v", servers[0])
	}
}

func TestFindClaudeSessions(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "-Users-jacob-projects-foo")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	recent := filepath.Join(proj, "abc-123.jsonl")
	if err := os.WriteFile(recent, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(proj, "old-999.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := now.Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	// a non-transcript file must be ignored
	if err := os.WriteFile(filepath.Join(proj, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := FindClaudeSessions(dir, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 recent session, got %d: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.ID != "abc-123" {
		t.Errorf("id = %q", got.ID)
	}
	if got.Cwd != "/Users/jacob/projects/foo" {
		t.Errorf("cwd = %q", got.Cwd)
	}
	if got.Path != recent {
		t.Errorf("path = %q", got.Path)
	}
}

func TestFindClaudeSessionsMissingDir(t *testing.T) {
	sessions, err := FindClaudeSessions(filepath.Join(t.TempDir(), "nope"), time.Hour, time.Now())
	if err != nil {
		t.Fatalf("missing dir should be nil error, got %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("want none, got %+v", sessions)
	}
}

func TestCombine(t *testing.T) {
	servers := []OpenCodeServer{{URL: "http://127.0.0.1:4096", PID: 111}}
	claude := []ClaudeSession{{ID: "cc-1", Cwd: "/tmp/x", Path: "/p/cc-1.jsonl"}}
	list := func(_ context.Context, url string) ([]protocol.Session, error) {
		if url != "http://127.0.0.1:4096" {
			t.Errorf("unexpected url %q", url)
		}
		return []protocol.Session{{ID: "ses_a", Title: "hello"}}, nil
	}

	items := combine(context.Background(), servers, claude, nil, list)

	// expect: 1 server + 1 opencode session + 1 claude session = 3
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(items), items)
	}
	if items[0].Kind != protocol.KindServer || items[0].Provider != "opencode" || items[0].URL != "http://127.0.0.1:4096" {
		t.Errorf("item0 = %+v", items[0])
	}
	if items[1].Kind != protocol.KindSession || items[1].SessionID != "ses_a" || items[1].Title != "hello" || items[1].URL != "http://127.0.0.1:4096" {
		t.Errorf("item1 = %+v", items[1])
	}
	if items[2].Provider != "claude-code" || items[2].SessionID != "cc-1" || items[2].Cwd != "/tmp/x" {
		t.Errorf("item2 = %+v", items[2])
	}
}

// TestCombineSessionCwd: a discovered session row carries the session's OWN directory, and only
// borrows the server's launch dir when opencode reported none. Pins the fix for takeover rows that
// showed no path and auto-registered the `opencode serve` cwd for sessions living in another
// folder/worktree entirely (one server serves sessions from many directories).
func TestCombineSessionCwd(t *testing.T) {
	orig := procCwd
	defer func() { procCwd = orig }()
	procCwd = func(context.Context, int) string { return "/repo" }

	servers := []OpenCodeServer{{URL: "http://127.0.0.1:4096", PID: 111}}
	list := func(context.Context, string) ([]protocol.Session, error) {
		return []protocol.Session{
			{ID: "ses_worktree", Cwd: "/repo/worktrees/feature-x"},
			{ID: "ses_nodir"}, // older opencode: no directory field → falls back to the server cwd
		}, nil
	}

	items := combine(context.Background(), servers, nil, nil, list)
	if len(items) != 3 {
		t.Fatalf("want server + 2 sessions, got %d: %+v", len(items), items)
	}
	if items[0].Cwd != "/repo" {
		t.Errorf("server cwd = %q, want /repo", items[0].Cwd)
	}
	if items[1].SessionID != "ses_worktree" || items[1].Cwd != "/repo/worktrees/feature-x" {
		t.Errorf("session row = %+v, want its own directory, not the server's", items[1])
	}
	if items[2].SessionID != "ses_nodir" || items[2].Cwd != "/repo" {
		t.Errorf("directory-less session = %+v, want the server cwd as fallback", items[2])
	}
}

func TestCombineSkipsUnreachableServer(t *testing.T) {
	servers := []OpenCodeServer{{URL: "http://127.0.0.1:1", PID: 1}}
	list := func(_ context.Context, _ string) ([]protocol.Session, error) {
		return nil, context.DeadlineExceeded
	}
	items := combine(context.Background(), servers, nil, nil, list)
	// still lists the server itself, just no sessions under it
	if len(items) != 1 || items[0].Kind != protocol.KindServer {
		t.Fatalf("want 1 server item, got %+v", items)
	}
}

// TestCombineThreadsContextToProcCwd ensures the scan context reaches procCwd so a
// cancelled/expired scan can abort the (potentially hanging) lsof lookup instead of the
// old ctx-ignoring exec.Command that stalled the whole scan.
func TestCombineThreadsContextToProcCwd(t *testing.T) {
	orig := procCwd
	defer func() { procCwd = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: procCwd must observe it

	sawCancel := false
	procCwd = func(c context.Context, _ int) string {
		sawCancel = c.Err() != nil
		return ""
	}
	servers := []OpenCodeServer{{URL: "http://127.0.0.1:4096", PID: 111}}
	list := func(_ context.Context, _ string) ([]protocol.Session, error) { return nil, nil }

	combine(ctx, servers, nil, nil, list)
	if !sawCancel {
		t.Fatal("combine did not thread the scan context into procCwd")
	}
}

// TestReadTranscriptCwd_BeatsLossyDecode: when the transcript records the real cwd, it's
// used instead of the lossy dir-name decode (which mangles paths containing '-').
func TestReadTranscriptCwd_BeatsLossyDecode(t *testing.T) {
	dir := t.TempDir()
	// Dir name encodes /Users/jacob/my-project as -Users-jacob-my-project → decode is lossy
	// (would yield /Users/jacob/my/project). The transcript has the true cwd.
	proj := filepath.Join(dir, "-Users-jacob-my-project")
	_ = os.MkdirAll(proj, 0o755)
	tx := filepath.Join(proj, "s1.jsonl")
	_ = os.WriteFile(tx, []byte(`{"type":"user","cwd":"/Users/jacob/my-project"}`+"\n"), 0o644)

	sessions, err := FindClaudeSessions(dir, 24*time.Hour, time.Now())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %+v err=%v", sessions, err)
	}
	if sessions[0].Cwd != "/Users/jacob/my-project" {
		t.Errorf("cwd = %q, want /Users/jacob/my-project (real, not lossy decode)", sessions[0].Cwd)
	}
}
