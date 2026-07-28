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
