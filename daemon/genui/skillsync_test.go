package genui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoSkillInSync guarantees the human-browsable portable skill (skills/iron-ui/SKILL.md, fanned
// out by scripts/sync-skills.sh) never drifts from the daemon's embedded canonical copy. If this
// fails, run: go test ./daemon/genui -run TestRepoSkillInSync -update, or copy daemon/genui/skill.md
// to skills/iron-ui/SKILL.md.
func TestRepoSkillInSync(t *testing.T) {
	repoSkill := filepath.Join("..", "..", "skills", "iron-ui", "SKILL.md")
	b, err := os.ReadFile(repoSkill)
	if err != nil {
		t.Fatalf("read repo skill: %v (run SyncRepoSkill)", err)
	}
	if string(b) != SkillMarkdown() {
		t.Fatalf("skills/iron-ui/SKILL.md is out of sync with daemon/genui/skill.md — regenerate it")
	}
}

// TestGuideBodyStripsFrontmatter confirms the injected guide is the instructional body only (no YAML).
func TestGuideBodyStripsFrontmatter(t *testing.T) {
	p := Preamble()
	if strings.Contains(p, "description:") || strings.Contains(p, "\n---\n") {
		t.Fatalf("preamble leaked frontmatter: %q", p)
	}
	if !strings.Contains(p, "iron:ui") {
		t.Fatal("preamble missing the iron:ui grammar")
	}
}

// TestInstallNativeSkillsGatedAndIdempotent verifies native install only touches present harnesses,
// writes the Agent Skills format for claude-code/pi, a marker block for codex, and is idempotent.
func TestInstallNativeSkillsGatedAndIdempotent(t *testing.T) {
	home := t.TempDir()
	// Nothing present yet → no installs, no surprise dirs.
	if notes := InstallNativeSkills(home); len(notes) != 0 {
		t.Fatalf("expected no installs into an empty home, got %v", notes)
	}
	// Make the harness dirs present.
	mustMkdir(t, filepath.Join(home, ".claude"))
	mustMkdir(t, filepath.Join(home, ".pi", "agent"))
	mustMkdir(t, filepath.Join(home, ".codex"))

	notes := InstallNativeSkills(home)
	if len(notes) != 3 {
		t.Fatalf("expected 3 installs (claude/pi/codex), got %d: %v", len(notes), notes)
	}
	// claude-code + pi got the canonical SKILL.md.
	for _, p := range []string{
		filepath.Join(home, ".claude", "skills", "iron-ui", "SKILL.md"),
		filepath.Join(home, ".pi", "agent", "skills", "iron-ui", "SKILL.md"),
	} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != SkillMarkdown() {
			t.Fatalf("skill not installed correctly at %s (err=%v)", p, err)
		}
	}
	// codex got a marker block containing the grammar.
	ag, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil || !strings.Contains(string(ag), markerBegin) || !strings.Contains(string(ag), "iron:ui") {
		t.Fatalf("codex AGENTS.md missing the managed block (err=%v)", err)
	}
	// Idempotent: a second run changes nothing.
	if notes := InstallNativeSkills(home); len(notes) != 0 {
		t.Fatalf("second install should be a no-op, got %v", notes)
	}
}

func TestUpsertMarkerBlockReplacesInPlace(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# existing\nkeep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, _ := upsertMarkerBlock(path, "VERSION ONE"); !changed {
		t.Fatal("first upsert should change the file")
	}
	if changed, _ := upsertMarkerBlock(path, "VERSION TWO"); !changed {
		t.Fatal("second upsert with new body should change the file")
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "keep me") {
		t.Fatal("upsert clobbered pre-existing content")
	}
	if strings.Contains(s, "VERSION ONE") || !strings.Contains(s, "VERSION TWO") {
		t.Fatalf("upsert did not replace the block in place: %q", s)
	}
	if strings.Count(s, markerBegin) != 1 {
		t.Fatalf("expected exactly one managed block, got %d", strings.Count(s, markerBegin))
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
