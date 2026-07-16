#!/usr/bin/env bash
# Fan the portable skills in skills/ into each detected agent's native location,
# so claude-code / opencode / codex / cursor can use them first-class.
#
# Skills stay authored once (skills/<name>/SKILL.md); this just links them out.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKILLS="$ROOT/skills"

link() { # link <dest-dir>
  local dest="$1"
  mkdir -p "$dest"
  for d in "$SKILLS"/*/; do
    [ -f "$d/SKILL.md" ] || continue
    local name; name="$(basename "$d")"
    rm -rf "$dest/$name"
    ln -s "$d" "$dest/$name"
    echo "  linked $name -> $dest/"
  done
}

echo "Syncing Oculus skills from $SKILLS"

# claude-code (project-local)
link "$ROOT/.claude/skills"

# opencode / others read AGENTS.md natively; add more targets here as needed.
# If the cross-agent 'skills' CLI is available, prefer it:
#   npx skills add "$ROOT"

echo "Done. (Base instructions: AGENTS.md — every agent reads it.)"
