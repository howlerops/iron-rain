package genui

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

// skill.md is the ONE canonical source for the iron:ui grammar. Everything else derives from it, so
// nothing drifts: the daemon's first-turn injection (Preamble), the repo skill folder
// (skills/iron-ui/ via SyncRepoSkill), and the per-session native materialization (MaterializeSkill)
// all read this same embedded file.
//
//go:embed skill.md
var skillMarkdown string

// The generative-UI "skill" reaches an agent one of two ways: (1) NATIVE — materialized into a
// project's .claude/skills (see MaterializeSkill), lazily loaded by harnesses that support skills; or
// (2) INJECTED — the compact guide folded into a session's first user turn (see Preamble), the
// universal fallback that works on every harness down to a one-shot CLI. Both come from skill.md.
const (
	GuideOpen  = "⟦iron:ui-guide⟧"
	GuideClose = "⟦/iron:ui-guide⟧"
)

// SkillMarkdown returns the canonical SKILL.md (frontmatter + body) — the single source of truth.
func SkillMarkdown() string { return skillMarkdown }

// guideBody returns the skill's instructional body with the YAML frontmatter stripped — what the
// model actually needs. Used by Preamble for the first-turn injection.
func guideBody() string {
	s := skillMarkdown
	if strings.HasPrefix(s, "---") {
		if end := strings.Index(s[3:], "\n---"); end >= 0 {
			s = s[3+end+len("\n---"):]
		}
	}
	return strings.TrimSpace(s)
}

// Preamble returns the sentinel-wrapped guide to prepend to a session's first user turn. The app
// strips everything between the sentinels from display (see Model.stripUIGuide).
func Preamble() string {
	return GuideOpen + "\n" + guideBody() + "\n" + GuideClose + "\n\n"
}

// StripGuide removes any injected guide block from text (the app does the same for display). Safe on
// text without a guide.
func StripGuide(s string) string {
	for {
		i := strings.Index(s, GuideOpen)
		if i < 0 {
			return s
		}
		j := strings.Index(s, GuideClose)
		if j < 0 || j < i {
			return s
		}
		end := j + len(GuideClose)
		for end < len(s) && (s[end] == '\n' || s[end] == '\r' || s[end] == ' ' || s[end] == '\t') {
			end++
		}
		s = s[:i] + s[end:]
	}
}

// MaterializeSkill installs the iron:ui skill natively for a session running in `cwd`, so a
// skill-aware harness (claude-code and friends) loads it lazily instead of us paying the injection's
// per-session tokens. It is CONSERVATIVE by design — it only writes where the harness's skill
// infrastructure ALREADY exists (a `.claude` dir), so it never creates surprise directories or
// mutates a repo that isn't using skills. Returns the paths written (may be empty). Idempotent:
// rewrites only when the content differs. The first-turn injection remains the universal fallback.
func MaterializeSkill(cwd string) []string {
	if cwd == "" {
		return nil
	}
	var written []string
	// claude-code: project-local .claude/skills/iron-ui/SKILL.md (only if .claude already exists).
	if dir := filepath.Join(cwd, ".claude"); isDir(dir) {
		if p, ok := writeSkillFile(filepath.Join(dir, "skills", "iron-ui", "SKILL.md")); ok {
			written = append(written, p)
		}
	}
	return written
}

// SyncRepoSkill mirrors the canonical skill.md into the repo's portable skills/ folder
// (skills/iron-ui/SKILL.md) so `scripts/sync-skills.sh` fans it out to agents working ON Iron Rain,
// and so the human-browsable skill can't drift from the daemon's embedded copy. Call from a generator
// or a keep-in-sync test with the repo root.
func SyncRepoSkill(repoRoot string) (string, error) {
	dest := filepath.Join(repoRoot, "skills", "iron-ui", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, []byte(skillMarkdown), 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

func writeSkillFile(path string) (string, bool) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == skillMarkdown {
		return path, false // already in sync
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false
	}
	if err := os.WriteFile(path, []byte(skillMarkdown), 0o644); err != nil {
		return "", false
	}
	return path, true
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
