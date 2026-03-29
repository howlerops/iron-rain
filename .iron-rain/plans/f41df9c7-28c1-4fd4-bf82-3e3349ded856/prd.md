I now have a clear picture of the problem. Here's the PRD:

---

# PRD: Per-Implementation Persistent Input History

## Title
Persistent Input History per Project Implementation

## Description
Iron-rain's input history (up/down arrow navigation) is currently derived from `state.messages` in the active session. This means:

1. **New sessions start with no history** — when you run `/new` or open a fresh terminal, up-arrow does nothing even though you've sent dozens of prompts in that project before.
2. **History is session-scoped, not implementation-scoped** — each terminal instance running iron-rain in a project directory should share a persistent input history (like shell history or Claude Code's `~/.claude/` history).
3. **History doesn't survive across sessions** — the current approach reads `state.messages.filter(m => m.role === "user")` which is only populated for the current/resumed session.

The fix: introduce a dedicated **input history** store, scoped per project directory, that persists across all sessions in that project. Pressing up-arrow in a fresh terminal should recall prior inputs from any session in that project.

## Requirements

### Must Have
1. **Dedicated input history table** in `sessions.db` — stores raw user input strings with timestamps, scoped by project directory (cwd).
2. **Cross-session persistence** — history entries survive session boundaries. Opening a new session in the same project directory loads the full input history.
3. **Up/down arrow navigation** uses the dedicated history store instead of `state.messages` — works identically to current behavior but sources from the persistent store.
4. **Per-project scoping** — different project directories maintain separate histories. The key is the working directory path.
5. **Deduplication** — consecutive duplicate inputs are not stored twice (like `HISTCONTROL=ignoredups` in bash).
6. **Reasonable history limit** — cap at ~1000 entries per project, oldest entries pruned automatically.

### Should Have
7. **Immediate write** — each submitted input is persisted to the history table synchronously (or at least before the next prompt), so it's available even if the process crashes mid-dispatch.
8. **History available on startup** — when entering a session route (new or resumed), the full project history is loaded and navigable immediately.

### Won't Have (this iteration)
9. History search (Ctrl+R style) — future enhancement.
10. History editing/deletion commands — future enhancement.
11. Cross-project global history — each project is isolated.

## Technical Approach

### 1. New SQLite Table: `input_history`
Add to `session-db.ts` → `initDb()`:

```sql
CREATE TABLE IF NOT EXISTS input_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_dir TEXT NOT NULL,
  content TEXT NOT NULL,
  timestamp INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_input_history_project
  ON input_history(project_dir, timestamp DESC);
```

### 2. New `SessionDB` Methods
- `addInputHistory(projectDir: string, content: string): void` — inserts entry, deduplicates against the last entry for that project, prunes if over 1000.
- `getInputHistory(projectDir: string, limit?: number): string[]` — returns inputs oldest-to-newest (so index 0 is oldest, last is most recent).

### 3. Wire into Context (`slate-context.tsx`)
- On init, load `db.getInputHistory(cwd)` into a signal/store (e.g., `inputHistory: string[]`).
- In `addMessage()`, when `msg.role === "user"`, also call `db.addInputHistory(cwd, msg.content)` and append to the in-memory `inputHistory` array.
- Expose `inputHistory()` accessor from context actions.

### 4. Update History Navigation (`session.tsx`)
Replace the current `state.messages.filter(m => m.role === "user")` source:

```typescript
// Before:
const userMessages = state.messages.filter(m => m.role === "user").map(m => m.content);

// After:
const userMessages = actions.inputHistory();
```

Everything else (historyIndex, savedDraft, up/down logic) stays the same.

### 5. NullSessionDB
Add no-op implementations of `addInputHistory` and `getInputHistory` to `NullSessionDB`.

### Files to Modify
| File | Change |
|------|--------|
| `packages/tui/src/store/session-db.ts` | Add `input_history` table, `addInputHistory()`, `getInputHistory()` methods |
| `packages/tui/src/context/slate-context.tsx` | Load history on init, persist on message add, expose accessor |
| `packages/tui/src/routes/session.tsx` | Source history from context instead of `state.messages` |

## Out of Scope
- **Ctrl+R fuzzy search** — separate feature, not needed for basic up/down navigation
- **History file format** (e.g., `~/.iron-rain/history`) — SQLite is already the persistence layer, no need for a separate file
- **Multi-terminal real-time sync** — if two terminals are open in the same project, they each load history on startup; new entries from one won't appear in the other until restart (acceptable for now)
- **History import/export** — no CLI commands for managing history
- **Encryption of history entries** — inputs are stored as plaintext, same as messages today