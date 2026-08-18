package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/worktree"
)

// synthPeer builds a managedSession stub carrying just the worktree metadata synthPrompt reads.
func synthPeer(t *testing.T, name, wtPath, base string, variant int) *managedSession {
	t.Helper()
	m := &managedSession{}
	m.meta.workspaceName = name
	m.meta.worktreePath = wtPath
	m.meta.baseCommit = base
	m.meta.fanoutVariant = variant
	return m
}

// TestSynthPromptCarriesEachVariantsDiff: the synthesiser is the ONE place raw diffs belong in a
// prompt — the judge deliberately reads only summaries, but you cannot combine code you cannot see.
func TestSynthPromptCarriesEachVariantsDiff(t *testing.T) {
	root, head := gcRepoForSynth(t)
	a := makeVariant(t, root, head, "variant-a", "func A() {}\n")
	b := makeVariant(t, root, head, "variant-b", "func B() {}\n")

	prompt, sources := synthPrompt(context.Background(), "add a function",
		[]*managedSession{synthPeer(t, "variant-a", a, head, 0), synthPeer(t, "variant-b", b, head, 1)}, nil)

	if len(sources) != 2 {
		t.Fatalf("included %d diffs, want 2", len(sources))
	}
	for _, want := range []string{"func A()", "func B()", "add a function"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q — the agent cannot combine what it was not shown", want)
		}
	}
}

// TestSynthPromptSaysWhatItOmitted: an agent told it has every attempt will reason as though it
// does. A diff dropped for size has to be NAMED, not silently missing, or the synthesis will
// confidently claim to have considered work it never saw.
func TestSynthPromptSaysWhatItOmitted(t *testing.T) {
	root, head := gcRepoForSynth(t)
	big := makeVariant(t, root, head, "variant-big", strings.Repeat("// filler line\n", maxSynthDiffBytes/15+500))
	small := makeVariant(t, root, head, "variant-small", "func S() {}\n")

	prompt, sources := synthPrompt(context.Background(), "task",
		[]*managedSession{synthPeer(t, "variant-big", big, head, 0), synthPeer(t, "variant-small", small, head, 1)}, nil)

	if len(sources) != 1 {
		t.Fatalf("included %d diffs, want 1 (the oversized one must be skipped)", len(sources))
	}
	if !strings.Contains(prompt, "variant-big") || !strings.Contains(prompt, "too large") {
		t.Fatal("the oversized variant was dropped without saying so in the prompt")
	}
	// It must still carry the SHAPE of what was skipped — a diffstat — so the synthesiser knows an
	// attempt exists and roughly what it did, rather than only that something is missing.
	if !strings.Contains(prompt, "files, +") {
		t.Fatal("an omitted variant carried no diffstat; the agent learns nothing about what it missed")
	}
	// And it must be skipped whole — a half-diff looks complete and silently loses the rest.
	if strings.Contains(prompt, "filler line") {
		t.Fatal("an oversized diff was truncated into the prompt instead of skipped")
	}
}

// TestSynthPromptWarnsAgainstBlindConcatenation: combining two coherent designs is frequently worse
// than either, which is exactly why the existing judge refuses to auto-merge. The instruction has to
// carry that, or the synthesis becomes the bad merge the judge was avoiding.
func TestSynthPromptWarnsAgainstBlindConcatenation(t *testing.T) {
	root, head := gcRepoForSynth(t)
	a := makeVariant(t, root, head, "va", "func A() {}\n")
	b := makeVariant(t, root, head, "vb", "func B() {}\n")
	prompt, _ := synthPrompt(context.Background(), "t",
		[]*managedSession{synthPeer(t, "va", a, head, 0), synthPeer(t, "vb", b, head, 1)}, nil)

	for _, want := range []string{"incompatible", "pick the better one"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not warn against splicing incompatible approaches (missing %q)", want)
		}
	}
	// It must also be told its worktree is EMPTY — it starts from base, not from a sibling's work.
	if !strings.Contains(prompt, "none of it is") && !strings.Contains(prompt, "Do not assume") {
		t.Fatal("prompt does not tell the agent its worktree starts clean")
	}
}

// gcRepoForSynth builds a one-commit repo; makeVariant adds a worktree with a committed change so
// `git diff base..HEAD` returns something real.
func gcRepoForSynth(t *testing.T) (root, head string) {
	t.Helper()
	root = t.TempDir()
	gitRun(t, root, "init", "-q")
	writeFile(t, filepath.Join(root, "seed.txt"), "seed\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "seed")
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(out))
}

func makeVariant(t *testing.T, root, head, name, body string) string {
	t.Helper()
	wt, err := worktree.Create(t.TempDir(), root, name)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt.Path, name+".go"), body)
	gitRun(t, wt.Path, "add", "-A")
	gitRun(t, wt.Path, "commit", "-qm", "work")
	return wt.Path
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSynthesisReArmsTheDoneLatch is the regression for the bug that made this feature invisible.
//
// checkFanoutDone fires ONCE per group (hub.go's fanoutNotified guard), and it has already fired by
// the time anyone can tap Combine — the comparison screen only exists because the originals
// finished. Adding the synthesis to that group without clearing the latch meant its completion hit
// the guard and returned: no summary rebroadcast, no judge, no card. The agent did a full turn of
// real work in a real worktree and the result went nowhere, silently, which is worse than not
// offering the feature at all.
func TestSynthesisReArmsTheDoneLatch(t *testing.T) {
	h := New()
	h.fanoutNotified["g1"] = true // the originals finished; the comparison has been shown

	// What synthesizeFanout does after successfully starting the variant.
	h.mu.Lock()
	delete(h.fanoutNotified, "g1")
	h.mu.Unlock()

	h.mu.Lock()
	latched := h.fanoutNotified["g1"]
	h.mu.Unlock()
	if latched {
		t.Fatal("the done-latch is still set, so the synthesis result would never be broadcast")
	}
}
