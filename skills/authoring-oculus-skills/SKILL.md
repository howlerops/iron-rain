---
name: authoring-oculus-skills
description: How to write and maintain Oculus skills so any coding agent (claude-code, opencode, codex, pi, cursor) can work on Oculus first-class. Use when adding or changing a subsystem.
---

# Authoring Oculus skills

Oculus is meant to be worked on long-term by **any** coding agent. The mechanism is a folder of
portable skills that get synced into each agent's native location.

## The rule (definition of done)
Every component change ships with **three** things in the *same* change:
1. **Code** (+ tests for logic/crypto/protocol).
2. A section in the nearest **`AGENTS.md`** (root or package).
3. A **`SKILL.md`** in `skills/<skill-name>/` describing how to use/extend the component.

If you add a subsystem without a skill, the change is not done.

## Skill format (portable — Agent Skills spec)
```
skills/<kebab-name>/
  SKILL.md            # required: frontmatter + instructions
  <supporting files>  # optional: scripts, templates, examples
```
`SKILL.md` frontmatter:
```yaml
---
name: <kebab-name>            # matches the folder
description: <one line — what it's for and WHEN to use it>
---
```
Then the body: concise, imperative instructions. Link related skills as `skills/<name>`.

## Keep it good
- Description is a *trigger*: say **when** an agent should reach for this skill, not just what it is.
- Prefer showing the exact commands/paths over prose.
- Update the skill in the same change as the code — never let them drift.

## Syncing into agents
`scripts/sync-skills.sh` copies `skills/` into each detected agent's location (e.g.
`.claude/skills/`), or use `npx skills` if the ecosystem tool is installed. Base instructions come
from `AGENTS.md` (every agent reads it; `CLAUDE.md` includes it).
