package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestReal_ThreadTreeFromClaudeTranscript reads an ACTUAL claude-code transcript from this machine.
//
// Against a synthetic fixture this would only prove the parser matches the fixture. The shapes that
// matter here — tool results recorded as "user" entries, sidechain sub-agent branches, the injected
// generative-UI guide on the first turn, content as either a string or typed blocks — are all things
// real transcripts contain and a fixture would have to be guessed into existence.
func TestReal_ThreadTreeFromClaudeTranscript(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	root := filepath.Join(home, ".claude", "projects")
	matches, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	// Find a substantial transcript that has HUMAN prompts in it.
	//
	// Not merely the first large one: an automated session (one driven entirely by
	// task-notifications) legitimately contains no human turns at all, and correctly yields zero
	// branch points. Picking one of those would fail this test for the right behaviour — which it
	// did, the first time it ran.
	prov := &Provider{resume: map[string]string{}}
	prov.SetProjectsDir(root)
	var pick string
	var nodes []protocol.ThreadNode
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || fi.Size() < 200*1024 {
			continue
		}
		uuid := strings.TrimSuffix(filepath.Base(m), ".jsonl")
		s := &session{id: uuid, p: prov, replayUUID: uuid, forks: newForkWaiters()}
		got, err := s.ThreadTree(context.Background())
		if err == nil && len(got) > 0 {
			pick, nodes = m, got
			break
		}
	}
	if pick == "" {
		t.Skip("no claude transcript with human prompts on this machine")
	}

	current, onPath := 0, 0
	for i, n := range nodes {
		if n.ID == "" {
			t.Errorf("node %d has no uuid, so it cannot be forked from", i)
		}
		if strings.TrimSpace(n.Preview) == "" {
			t.Errorf("node %d would render as a blank row", i)
		}
		// The injected generative-UI guide must never be what a row shows.
		if strings.Contains(n.Preview, "iron:ui") {
			t.Errorf("node %d previews the injected guide rather than the prompt: %q", i, n.Preview)
		}
		if n.Current {
			current++
		}
		if n.OnPath {
			onPath++
		}
	}
	if current != 1 {
		t.Errorf("%d nodes marked current, want exactly 1", current)
	}
	if onPath == 0 {
		t.Error("no nodes on the active path — the leaf walk found nothing, so every branch would render dimmed")
	}
	t.Logf("%s: %d branch points, %d on the active path; first=%q",
		filepath.Base(pick), len(nodes), onPath, truncate(nodes[0].Preview, 60))
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
