package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A claude-code transcript, in the real on-disk shape: every entry records the working directory the
// conversation belongs to.
const claudeJSONL = `{"type":"summary","summary":"stuff"}
{"type":"user","uuid":"u1","cwd":"__PROJECT__","message":{"role":"user","content":"fix the auth bug"}}
{"type":"assistant","uuid":"a1","cwd":"__PROJECT__","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}
`

// fakeSidecarPrintsCwd stands in for the Node sidecar: it records the directory it was started in
// (which is where claude's edits would land) and then idles on stdin.
const fakeSidecarPrintsCwd = `#!/bin/sh
pwd -P > "$CC_CWD_OUT"
while IFS= read -r line; do :; done
`

// TestAttachWithoutACwdResumesInTheSessionsOwnProject: the SDK's resume runs as a FRESH process and
// edits whatever directory it is started in — not the one the conversation belongs to. When the
// daemon has no recorded cwd (an older record, or a take-over discovered by transcript), attaching
// used to start the sidecar in the DAEMON's directory, so a resumed session quietly began editing
// the wrong repository. The transcript itself records the project, so use it.
func TestAttachWithoutACwdResumesInTheSessionsOwnProject(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	project := t.TempDir()
	projects := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projects, "-some-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(claudeJSONL, "__PROJECT__", project)
	if err := os.WriteFile(filepath.Join(projects, "-some-project", uuid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-sidecar.sh")
	if err := os.WriteFile(script, []byte(fakeSidecarPrintsCwd), 0o755); err != nil {
		t.Fatal(err)
	}
	cwdOut := filepath.Join(dir, "cwd.txt")
	t.Setenv("CC_CWD_OUT", cwdOut)

	p := New([]string{script})
	p.SetProjectsDir(projects)
	sess, err := p.Attach(context.Background(), uuid, "") // no recorded cwd
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var got []byte
	for wait := time.Now().Add(10 * time.Second); time.Now().Before(wait); {
		if b, err := os.ReadFile(cwdOut); err == nil && len(b) > 0 {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want, _ := filepath.EvalSymlinks(project)
	if !strings.Contains(string(got), want) {
		t.Fatalf("sidecar started in %q, want the session's own project %q — a resumed claude session would edit the wrong repo", strings.TrimSpace(string(got)), want)
	}
}
