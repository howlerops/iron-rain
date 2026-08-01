package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A pi session file, in pi's real v3 format (docs/session-format.md): a header line naming the
// session id + cwd, then message entries carrying role and content blocks.
const piSessionJSONL = `{"type":"session","version":3,"id":"019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e","timestamp":"2026-06-27T20:41:37.022Z","cwd":"/Users/x/proj"}
{"type":"model_change","id":"cbfccedd","parentId":null,"provider":"anthropic","modelId":"claude-opus-4"}
{"type":"message","id":"f862609a","parentId":"cbfccedd","message":{"role":"user","content":[{"type":"text","text":"what changed?"}],"timestamp":1782592898186}}
{"type":"message","id":"179b79cc","parentId":"f862609a","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"three files"}],"provider":"anthropic","model":"claude-opus-4","timestamp":1782592899958}}
`

// writePiSession lays out a sessions root exactly as pi does: ~/.pi/agent/sessions/--<path>--/<ts>_<uuid>.jsonl
func writePiSession(t *testing.T, id, cwd string) (root, path string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "--Users-x-proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "2026-06-27T20-41-37-022Z_"+id+".jsonl")
	body := strings.ReplaceAll(piSessionJSONL, "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e", id)
	body = strings.ReplaceAll(body, "/Users/x/proj", cwd)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, path
}

// TestFindSessionFileLocatesAPiSessionOnDisk: pi keeps every conversation as a JSONL file under
// ~/.pi/agent/sessions, so — unlike a generic CLI — a pi session CAN be resumed after the daemon
// dies. Finding that file by session id is the whole basis of it.
func TestFindSessionFileLocatesAPiSessionOnDisk(t *testing.T) {
	root, path := writePiSession(t, "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e", "/Users/x/proj")

	if got := findSessionFile(root, "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e"); got != path {
		t.Errorf("findSessionFile = %q, want %q", got, path)
	}
	if got := findSessionFile(root, "nope"); got != "" {
		t.Errorf("findSessionFile for an unknown id = %q, want empty", got)
	}
}

// TestDiscoverListsOnDiskSessions: takeover has to be able to SEE a pi conversation the user ran in
// a terminal — id, project directory and recency all come from the file itself.
func TestDiscoverListsOnDiskSessions(t *testing.T) {
	root, _ := writePiSession(t, "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e", "/Users/x/proj")

	found := Discover(root, time.Hour*24*3650, time.Now())
	if len(found) != 1 {
		t.Fatalf("found %d pi sessions, want 1", len(found))
	}
	if found[0].ID != "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e" {
		t.Errorf("id = %q", found[0].ID)
	}
	if found[0].Cwd != "/Users/x/proj" {
		t.Errorf("cwd = %q, want the directory recorded in the session header", found[0].Cwd)
	}
}

// TestCanResumeRequiresASessionFile: without a file there is nothing to resume, and "restoring" it
// would mint a fresh empty session lying about being the old one (the row that does nothing).
func TestCanResumeRequiresASessionFile(t *testing.T) {
	root, _ := writePiSession(t, "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e", "/Users/x/proj")
	p := New([]string{"/bin/true"})
	p.SetSessionsRoot(root)

	if !p.CanResume("019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e") {
		t.Error("CanResume = false for a session whose file is on disk")
	}
	if p.CanResume("pi_deadbeef") {
		t.Error("CanResume = true for a session with no file — restore would revive an empty impostor")
	}
}

// fakePiForAttach records the argv it was started with, then idles on stdin like a real rpc process.
const fakePiForAttach = `#!/bin/sh
printf '%s\n' "$*" > "$PI_ARGS_OUT"
pwd -P >> "$PI_ARGS_OUT"
while IFS= read -r line; do :; done
`

// TestAttachResumesTheSessionFileAndReplaysIt is the pi half of "takeover survives a restart":
// pi's RPC mode does not re-emit past messages, so an attach that only re-spawns the process shows
// an EMPTY conversation and — worse — starts a brand-new session, orphaning the old one on disk.
// The attach must point pi at the existing session file AND replay its history.
func TestAttachResumesTheSessionFileAndReplaysIt(t *testing.T) {
	const id = "019f0ad0-fffe-7ba7-b9ba-6c02c3aafb9e"
	project := t.TempDir() // the directory the conversation belongs to, per its own header
	root, path := writePiSession(t, id, project)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi.sh")
	if err := os.WriteFile(script, []byte(fakePiForAttach), 0o755); err != nil {
		t.Fatal(err)
	}
	argsOut := filepath.Join(dir, "argv.txt")
	t.Setenv("PI_ARGS_OUT", argsOut)

	p := New([]string{script, "--mode", "rpc"})
	p.SetSessionsRoot(root)
	// cwd is deliberately EMPTY: the restore may not know it, and pi must still resume in the
	// project the conversation belongs to rather than wherever the daemon happens to be running.
	sess, err := p.Attach(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var texts []string
	deadline := time.After(10 * time.Second)
	for len(texts) < 2 {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("event stream ended after %v", texts)
			}
			if ev.Type == protocol.TypeSessionMessage {
				msg := ev.Payload.(protocol.SessionMessage)
				texts = append(texts, msg.Role+":"+msg.Text)
			}
		case <-deadline:
			t.Fatalf("replay produced %v, want the on-disk conversation", texts)
		}
	}
	if texts[0] != "user:what changed?" || texts[1] != "assistant:three files" {
		t.Errorf("replayed %v, want the user turn then the assistant turn", texts)
	}

	// The child writes its argv asynchronously; poll rather than race it.
	var argv []byte
	for wait := time.Now().Add(10 * time.Second); time.Now().Before(wait); {
		if b, err := os.ReadFile(argsOut); err == nil && len(b) > 0 {
			argv = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(argv) == 0 {
		t.Fatal("fake pi never recorded its argv — it was not started")
	}
	if !strings.Contains(string(argv), "--session "+path) {
		t.Errorf("pi was started with %q — it must be pointed at the existing session file %q, or it starts a new conversation", strings.TrimSpace(string(argv)), path)
	}
	wantDir, _ := filepath.EvalSymlinks(project)
	if !strings.Contains(string(argv), wantDir) {
		t.Errorf("pi ran in the wrong directory (%q), want the session's own project %q — a resumed agent must edit the right repo", strings.TrimSpace(string(argv)), wantDir)
	}
}
