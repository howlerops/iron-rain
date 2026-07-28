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

// Marker sentinels for the codex/AGENTS.md managed block — matches codex's own convention of
// HTML-comment-delimited managed sections, so our block is clearly ours, updatable, and removable.
const (
	markerBegin = "<!-- iron:ui BEGIN (managed by Iron Rain — safe to delete) -->"
	markerEnd   = "<!-- iron:ui END -->"
)

// InstallNativeSkills installs the iron:ui skill into each PRESENT harness's native location under
// `home`, so a skill-aware harness lazy-loads it instead of us paying the first-turn injection's
// tokens. It is CONSERVATIVE and idempotent: each target is gated on that harness's own directory
// already existing (never creates surprise trees), and a file is rewritten only when its content
// differs. Returns human-readable notes for what changed. opencode has no global skills directory, so
// it (and any harness we don't cover) relies on the first-turn injection, which remains universal.
func InstallNativeSkills(home string) []string {
	if home == "" {
		return nil
	}
	var notes []string
	// claude-code + pi share the Agent Skills format (a SKILL.md under a skills dir). Install only if
	// the harness's parent dir already exists.
	for _, t := range []struct{ name, parent, skills string }{
		{"claude-code", filepath.Join(home, ".claude"), filepath.Join(home, ".claude", "skills")},
		{"pi", filepath.Join(home, ".pi", "agent"), filepath.Join(home, ".pi", "agent", "skills")},
	} {
		if !isDir(t.parent) {
			continue
		}
		if p, ok := writeSkillFile(filepath.Join(t.skills, "iron-ui", "SKILL.md")); ok {
			notes = append(notes, t.name+" → "+p)
		}
	}
	// codex reads a global ~/.codex/AGENTS.md and already uses managed marker blocks there, so we
	// upsert our grammar as one clearly-delimited section rather than a skills folder.
	if dir := filepath.Join(home, ".codex"); isDir(dir) {
		path := filepath.Join(dir, "AGENTS.md")
		if changed, err := upsertMarkerBlock(path, guideBody()); err == nil && changed {
			notes = append(notes, "codex → "+path)
		}
	}
	return notes
}

// upsertMarkerBlock ensures `path` contains exactly one iron:ui managed block wrapping `body`. It
// appends the block if absent and replaces it in place if present (and stale), leaving the rest of the
// file untouched. Returns whether the file changed. Creating the file is fine (codex's dir exists).
func upsertMarkerBlock(path, body string) (bool, error) {
	block := markerBegin + "\n" + body + "\n" + markerEnd
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	s := string(cur)
	i := strings.Index(s, markerBegin)
	j := strings.Index(s, markerEnd)
	var next string
	if i >= 0 && j > i {
		next = s[:i] + block + s[j+len(markerEnd):]
	} else {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		next = s + "\n" + block + "\n"
	}
	if next == s {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(next), 0o644)
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
