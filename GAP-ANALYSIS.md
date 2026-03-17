# Iron Rain — Competitive Gap Analysis

> Compared against: Claude Code, OpenCode, Codex CLI, Gemini CLI, Cursor, Windsurf, Aider, Continue.dev
> Date: 2026-03-17

---

## Executive Summary

Iron Rain's multi-model orchestration (Cortex/Scout/Forge) is a genuine differentiator — no competitor offers the same level of model-routing flexibility. The TUI, onboarding wizard, plan/loop workflows, and MCP support are solid. However, several **table-stakes** features are missing that competitors have standardized on, and there are high-value opportunities to pull ahead.

---

## P0 — Table Stakes (Missing features that competitors treat as baseline)

### 1. Git Checkpoint / Undo System
**Who has it:** OpenCode (snapshot at every tool call), Gemini CLI (git checkpoint snapshots), Codex CLI (sandbox rollback)
**Gap:** Iron Rain has no undo/rollback. If a tool call produces bad edits, the user must manually `git checkout` files. Every major competitor now offers automatic restore points.
**Recommendation:** Snapshot the working tree (via `git stash` or lightweight commits on a shadow branch) before each edit/write/bash tool call. Add `/undo` command to revert the last N tool actions.

### 2. Sandboxed Execution
**Who has it:** Claude Code (Seatbelt on macOS, Bubblewrap+seccomp on Linux), Codex CLI (3 modes: full-auto/suggest/ask with Docker), Gemini CLI (4 backends: Seatbelt/Docker/gVisor/LXC)
**Gap:** Iron Rain runs bash commands with no isolation. This is the #1 blocker for "full auto" mode and enterprise adoption.
**Recommendation:** Start with a `sandbox` config option. Phase 1: macOS Seatbelt profiles (network/fs restrictions). Phase 2: Docker container execution. Phase 3: configurable sandbox backends.

### 3. Project Rules / Instructions File
**Who has it:** Claude Code (CLAUDE.md + .claude/rules/), OpenCode (CLAUDE.md compat + .opencode), Cursor (`.cursorrules`), Continue.dev (`.continue/rules/`), Gemini CLI (GEMINI.md)
**Gap:** Iron Rain has no equivalent. Users can't provide persistent project-level instructions that survive across sessions.
**Recommendation:** Support `IRON-RAIN.md` (or `CLAUDE.md` for compatibility) at project root. Also support `.iron-rain/rules/*.md` for path-scoped rules. Load at session start, inject into system prompts.

### 4. Session Persistence & Restore
**Who has it:** Claude Code (session resume), OpenCode (session sharing via /share), Codex CLI (7-hour sessions), Gemini CLI (session history)
**Gap:** Iron Rain sessions are ephemeral. The SQLite DB stores lessons but not full conversation history. Users lose context on restart.
**Recommendation:** Persist full session history to SQLite. Add `/sessions` to list past sessions, `/resume <id>` to restore. Auto-resume last session on launch (with option to start fresh).

### 5. Auto-Commit on Tool Actions
**Who has it:** Gemini CLI (auto-commit after edits), Codex CLI (auto-commit in full-auto mode), Claude Code (via hooks)
**Gap:** Iron Rain's plan executor has auto-commit support, but regular tool calls don't. Users must manually commit.
**Recommendation:** Add `autoCommit` config option (default: off). When enabled, create a commit after each successful edit/write cycle with a descriptive message. Pair with the undo/checkpoint system.

---

## P1 — High Value (Features that would significantly differentiate or improve UX)

### 6. IDE / Editor Integration
**Who has it:** Claude Code (VS Code + JetBrains extensions), Cursor (native IDE), Windsurf (native IDE), Continue.dev (VS Code + JetBrains), OpenCode (desktop app)
**Gap:** Iron Rain is TUI-only. No way to use it from an editor.
**Recommendation:** VS Code extension that launches Iron Rain in an embedded terminal panel, with file-click integration (open files mentioned in responses). Lower effort than a full language server but high perceived value.

### 7. Parallel Subagent Execution
**Who has it:** Codex CLI (6 parallel subagent threads), Claude Code (/batch for parallel multi-file changes via worktrees), Cursor (up to 8 parallel background agents in worktrees)
**Gap:** Iron Rain's slots are sequential — Cortex delegates to Scout/Forge one at a time. For large tasks (multi-file refactors), this is slow.
**Recommendation:** Allow Cortex to dispatch multiple Scout/Forge tasks in parallel. Use git worktrees for isolation when edits might conflict. Show parallel progress in the subagent grid UI.

### 8. LSP Integration
**Who has it:** OpenCode (30+ language server integrations), Cursor (native LSP), Windsurf (native LSP)
**Gap:** Iron Rain has no awareness of language semantics. It can't jump to definitions, find references, or validate types beyond what grep provides.
**Recommendation:** Connect to workspace LSP servers (TypeScript, Python, Go, Rust). Use for: (a) smarter code navigation in Scout, (b) post-edit diagnostics in Forge (auto-fix lint/type errors), (c) repo-map generation for context.

### 9. Repository Map / Index
**Who has it:** Aider (ctags-based repo map), Cursor (codebase indexing), Continue.dev (codebase indexing)
**Gap:** Iron Rain has no persistent code index. Every search starts from scratch with grep/glob.
**Recommendation:** Generate a lightweight repo map on session start (file tree + exported symbols via ctags or tree-sitter). Include in system prompt for better code navigation. Regenerate on file changes.

### 10. Hook / Lifecycle System
**Who has it:** Claude Code (20+ hook events, 4 hook types: command/HTTP/prompt/agent), OpenCode (plugin lifecycle hooks)
**Gap:** Iron Rain has a basic plugin SDK with `beforeDispatch`/`afterDispatch` hooks, but no lifecycle events for tool calls, session events, or commit actions.
**Recommendation:** Expand hooks to cover: `onToolCall`, `onToolResult`, `onSessionStart`, `onSessionEnd`, `onCommit`, `onError`. Support shell command hooks (like Claude Code) for easy scripting without writing JS.

### 11. Headless / CI Mode Improvements
**Who has it:** Claude Code (headless + stream-json output), Codex CLI (`codex exec` for CI, CSV batch processing), OpenCode (GitHub Actions `/opencode` mentions)
**Gap:** Iron Rain has `--headless` but no structured output format (JSON streaming), no batch mode, no CI-specific features.
**Recommendation:** Add `--output json` for structured streaming output. Add `--batch <file>` for processing multiple prompts. Add exit codes that reflect success/failure of the task.

### 12. Auto-Memory / Learning
**Who has it:** Claude Code (auto memory — agent writes its own notes), Cursor (Memories), Windsurf (persistent memory layer)
**Gap:** Iron Rain has `/lessons` for manual persistent memory, but the agent doesn't automatically learn from sessions.
**Recommendation:** After each session, have Cortex summarize key learnings (patterns discovered, user preferences, project conventions) and store them as lessons. Load relevant lessons at session start.

---

## P2 — Nice to Have (Valuable but lower priority)

### 13. Multi-Surface Support
**Who has it:** Claude Code (Terminal, VS Code, JetBrains, Desktop, Web, Mobile)
**Gap:** TUI only.
**Recommendation:** Long-term goal. Start with a web UI (easier than native apps). The client/server split that OpenCode uses (serve/web/attach) is a good architecture model.

### 14. Tool Output Summarization
**Who has it:** Gemini CLI (summarizes long tool outputs to save context)
**Gap:** Iron Rain passes full tool output to models, which can blow context windows.
**Recommendation:** For tool outputs exceeding a threshold (e.g., 2000 tokens), have Scout summarize the output before passing it to the next dispatch.

### 15. Voice Mode
**Who has it:** Aider (voice input for prompts)
**Gap:** No voice support.
**Recommendation:** Low priority, but could be a quick win using system speech-to-text APIs.

### 16. Custom Slash Commands with Arguments
**Who has it:** OpenCode (custom slash commands with `$ARGUMENTS` substitution)
**Gap:** Iron Rain skills support arguments, but custom user-defined slash commands (simpler than full skills) aren't supported.
**Recommendation:** Allow `/.iron-rain/commands/` directory with simple template files that expand `$ARGUMENTS`. Lower barrier than writing a full skill.

### 17. Remote Config / Team Sharing
**Who has it:** OpenCode (`.well-known/opencode` for org-wide config), Claude Code (5-tier managed enterprise policies)
**Gap:** Config is per-machine only.
**Recommendation:** Support a `configUrl` field that fetches remote config (team defaults). Merge with local overrides.

### 18. Built-in Code Review
**Who has it:** Codex CLI (`/review` command), Cursor (BugBot PR reviewer)
**Gap:** No dedicated review workflow.
**Recommendation:** Add `/review` that analyzes staged changes or a PR diff, using Cortex for strategic review and Scout for line-by-line analysis.

### 19. Model Cost Tracking
**Who has it:** Most tools show token counts; few show actual cost.
**Gap:** Iron Rain shows tokens and timing but not estimated cost.
**Recommendation:** Add per-model cost rates to the model registry. Show cumulative session cost in the stats bar.

### 20. Gemini CLI .geminiignore Equivalent
**Who has it:** Gemini CLI (`.geminiignore`)
**Gap:** No way to exclude files/directories from context injection.
**Recommendation:** Support `.ironrainignore` (or reuse `.gitignore` patterns) to exclude files from @ references and repo indexing.

---

## What Iron Rain Already Does Better

| Strength | vs. Competitors |
|----------|----------------|
| **Multi-model slot routing** | No competitor has true 3-slot orchestration with automatic tool-type routing |
| **Provider flexibility** | 7 bridges (4 API + 3 CLI) with mix-and-match. Most tools support 1-3 providers |
| **Plan & Loop workflows** | PRD generation + iterative loops with auto-retry. Unique combination |
| **Onboarding wizard** | Interactive setup flow. Most tools require manual config editing |
| **Thinking level control** | Per-slot thinking budgets mapped to provider-native params. Rare feature |
| **Subagent visibility** | Grid UI showing real-time slot activity. Most tools hide delegation |
| **MCP + Skills** | Combined MCP server support with discoverable skill files |

---

## Recommended Implementation Order

| Phase | Items | Effort | Impact |
|-------|-------|--------|--------|
| **Phase 1** (next sprint) | P0-3 (Project Rules), P0-5 (Auto-Commit), P1-12 (Auto-Memory) | Low-Med | High — immediate UX wins |
| **Phase 2** (following sprint) | P0-1 (Git Undo), P0-4 (Session Persistence), P1-11 (Headless/CI) | Medium | High — reliability + CI adoption |
| **Phase 3** (2-3 sprints) | P0-2 (Sandboxing), P1-7 (Parallel Subagents), P1-10 (Hooks) | High | Critical for auto mode + extensibility |
| **Phase 4** (longer term) | P1-6 (IDE), P1-8 (LSP), P1-9 (Repo Map), P2-* | High | Competitive parity with IDE tools |

---

## Key Insight

The biggest strategic gap isn't any single feature — it's the **trust layer**. Sandboxing + undo + auto-commit together create a "let it run" experience that Claude Code, Codex, and Gemini CLI all offer. Until Iron Rain has that trust layer, users will hesitate to use full-auto mode. Phase 1-3 should prioritize building this trust stack.
