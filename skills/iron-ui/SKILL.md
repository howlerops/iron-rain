---
name: iron-ui
description: Render rich NATIVE UI (tables, checklists, callouts, diffs, choices) inline in an Iron Rain chat by emitting an iron:ui fenced block. Use when structured, quantitative, or selectable data is clearer as UI than prose.
---

# Iron Rain generative UI (iron:ui)

You are talking to a user through the **Iron Rain** app, which renders a small catalog of native UI
components. Emit one as a fenced block anywhere in your normal reply:

```iron:ui
{"component":"<type>","id":"<unique>","props":{ ... },"fallback_text":"<markdown>"}
```

Rules:
- One **valid JSON** object per fence. Put `component` and `id` first.
- ALWAYS include a short `fallback_text` (markdown) that conveys the same content — clients that can't
  render the component show it instead.
- Prefer normal markdown prose. Use a component ONLY when the data is genuinely clearer as UI. Emit at
  most a few per message.

## Catalog

- **table** — `props {columns:[{label, align?}], rows:[[cell, …]], caption?}`. Test results, file
  lists, comparisons.
- **checklist** — `props {title?, items:[{text, status:pending|active|done|failed}]}`. Plans, task
  status.
- **plan** — same props as **checklist**; use it when the list is a plan you intend to execute.
- **callout** — `props {level:info|warn|error|success, title?, body}`. A highlighted note.
- **diff** — `props {path, patch?}`. A unified-diff preview for one file.
- **choice** (interactive) — `props {prompt}` plus
  `actions:[{id, kind:"prompt", label, prompt:"<message sent AS THE USER'S REPLY when tapped>"}]`.
- **confirm** (interactive) — `props {prompt}` plus
  `actions:[{id, kind:"prompt", label, style:default|destructive, prompt:"<reply text>"}]`.

For **interactive** components, each action's `prompt` becomes the user's next turn when they tap it —
so phrase it from the user's side (e.g. `"Use approach B"`). A component can only start a new user
turn; it never runs a tool.

## Example

```iron:ui
{"component":"table","id":"tests","props":{"columns":[{"label":"Suite"},{"label":"Result","align":"right"}],"rows":[["auth","pass"],["api","2 failing"]]},"fallback_text":"auth: pass · api: 2 failing"}
```
