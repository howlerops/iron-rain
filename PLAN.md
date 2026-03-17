# Iron Rain — Comprehensive Implementation Plan

This plan addresses all 18 architecture improvements identified in the code review and all 20 competitive gap items from GAP-ANALYSIS.md. Items are deduplicated, cross-referenced, and organized into 5 phases with explicit dependencies. Every task includes exact file paths, implementation steps, and effort estimates.

**Total scope:** 32 tasks across 5 phases, covering 38 original items.

---

## Phase 0: Foundation (Code Quality & Refactoring)

Internal cleanup that enables everything else. No new features — just making the codebase ready for rapid feature development.

---

### F-01: Add Biome for Linting & Formatting

**Addresses:** Arch #2
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `biome.json` (root config)

**Files to modify:**
- `package.json` — add `@biomejs/biome` devDependency, add `format` and `lint` scripts
- `turbo.json` — update `lint` task to actually run biome
- `packages/core/package.json` — add `lint` script: `biome check src/`
- `packages/tui/package.json` — add `lint` script: `biome check src/`
- `packages/cli/package.json` — add `lint` script: `biome check src/`
- `packages/plugin/package.json` — add `lint` script: `biome check src/`

**Steps:**
1. Run `bun add -D @biomejs/biome` at root
2. Create `biome.json` with TypeScript + JSX support, import sorting, 2-space indent, single quotes, trailing commas
3. Add `"lint": "biome check src/"` to each package's `package.json` scripts
4. Add `"format": "biome format --write ."` to root `package.json`
5. Update `turbo.json` lint task: `{ "dependsOn": [] }` (remove empty object)
6. Run `bun run format` to auto-fix the entire codebase
7. Run `bun run lint` to verify zero violations
8. Add biome check to `.github/workflows/ci.yml` as a step before typecheck

---

### F-02: Add Test Framework + Initial Test Suite

**Addresses:** Arch #1
**Effort:** M (Medium)
**Dependencies:** None

**Files to create:**
- `packages/core/src/__tests__/bridge/anthropic.test.ts`
- `packages/core/src/__tests__/bridge/openai-compat.test.ts`
- `packages/core/src/__tests__/bridge/ollama.test.ts`
- `packages/core/src/__tests__/bridge/errors.test.ts`
- `packages/core/src/__tests__/orchestrator/kernel.test.ts`
- `packages/core/src/__tests__/orchestrator/worker.test.ts`
- `packages/core/src/__tests__/router/tool-router.test.ts`
- `packages/core/src/__tests__/router/heuristics.test.ts`
- `packages/core/src/__tests__/context/compaction.test.ts`
- `packages/core/src/__tests__/context/references.test.ts`
- `packages/core/src/__tests__/config/schema.test.ts`
- `packages/core/src/__tests__/config/loader.test.ts`
- `packages/core/src/__tests__/planner/generator.test.ts`
- `packages/core/src/__tests__/skills/loader.test.ts`
- `packages/core/src/__tests__/utils/id.test.ts`

**Files to modify:**
- `packages/core/package.json` — add `"test": "bun test"` script
- `turbo.json` — add `"test": { "dependsOn": ["build"] }` task
- `package.json` — add `"test": "turbo run test"` script
- `.github/workflows/ci.yml` — add test step

**Steps:**
1. Add `"test": "bun test"` to `packages/core/package.json` scripts (bun:test is built-in, no dep needed)
2. Add `test` task to `turbo.json`: `{ "dependsOn": ["build"] }`
3. Create test directory structure: `packages/core/src/__tests__/{bridge,orchestrator,router,context,config,planner,skills,utils}/`
4. Write `tool-router.test.ts` — test all 10 ToolType → SlotName mappings, test getToolsForSlot reverse lookup
5. Write `heuristics.test.ts` — test detectToolType for each ToolType pattern, test score threshold (>=2), test null return for ambiguous prompts
6. Write `compaction.test.ts` — test under-threshold passthrough, hot/cold split, keyword extraction, relevance scoring, RLM retrieval
7. Write `references.test.ts` — test slot prefix parsing, file resolution, dir listing, git whitelist, image detection, MAX_FILE_SIZE enforcement
8. Write `schema.test.ts` — test parseConfig with valid/invalid inputs, test resolveEnvValue with env: prefix
9. Write `loader.test.ts` — test findConfigFile, loadConfig with .json and .jsonc
10. Write `errors.test.ts` — test BridgeError.isRetryable() for 429, 500-599, 400-499, test CircuitBreaker threshold/reset, test backoffDelay jitter range
11. Write `kernel.test.ts` — test dispatch routing (explicit targetSlot, toolType routing, default to main), test orchestrate parallel, test episode recording
12. Write bridge tests with mocked fetch — test execute/stream for Anthropic, OpenAI, Ollama; test error responses
13. Write `id.test.ts` — test UUID v4 format, test uniqueness over 1000 generations
14. Add test step to CI: `- name: Test\n  run: bun run test`

---

### F-03: Extract BaseAPIBridge and BaseCLIBridge

**Addresses:** Arch #4
**Effort:** M (Medium)
**Dependencies:** F-02 (tests catch regressions)

**Files to create:**
- `packages/core/src/bridge/base-api.ts`
- `packages/core/src/bridge/base-cli.ts`

**Files to modify:**
- `packages/core/src/bridge/anthropic.ts`
- `packages/core/src/bridge/openai-compat.ts`
- `packages/core/src/bridge/ollama.ts`
- `packages/core/src/bridge/gemini.ts`
- `packages/core/src/bridge/claude-code.ts`
- `packages/core/src/bridge/codex.ts`
- `packages/core/src/bridge/gemini-cli.ts`
- `packages/core/src/bridge/index.ts` — re-export base classes
- `packages/core/src/index.ts` — export base classes

**Steps:**
1. Create `base-api.ts` with abstract class `BaseAPIBridge implements CLIBridge`:
   - Shared fields: `apiKey`, `apiBase`, `model`, `name`
   - Shared method: `available()` — check apiKey.length > 0
   - Shared method: `streamSSE(url, body, headers, options)` — the SSE reader/decoder/line-parser loop (currently ~40 lines duplicated 4x)
   - Shared method: `buildMessages(prompt, options)` — construct the messages array with systemPrompt + conversationHistory + user prompt
   - Abstract methods: `execute()`, `stream()`, `formatRequest()`, `parseResponse()`
2. Create `base-cli.ts` with abstract class `BaseCLIBridge implements CLIBridge`:
   - Shared method: `available()` — spawn `binaryPath --version`, check exit code 0 (currently identical in 3 files)
   - Shared method: `spawnAndCollect(args, signal)` — spawn process, collect stdout/stderr, resolve on close
   - Shared fields: `binaryPath`, `model`, `name`
3. Refactor `AnthropicBridge` to extend `BaseAPIBridge`:
   - Move SSE loop to `streamSSE()` call, override `parseChunk()` for Anthropic event format
   - Move message building to `buildMessages()` call with Anthropic system prompt format (top-level `system` field)
4. Refactor `OpenAICompatBridge` to extend `BaseAPIBridge`
5. Refactor `OllamaBridge` to extend `BaseAPIBridge`
6. Refactor `GeminiBridge` to extend `BaseAPIBridge` (with its unique content format)
7. Refactor `ClaudeCodeBridge` to extend `BaseCLIBridge` — use `spawnAndCollect()`
8. Refactor `CodexBridge` to extend `BaseCLIBridge`
9. Refactor `GeminiCLIBridge` to extend `BaseCLIBridge`
10. Run tests to verify no behavior change
11. Update exports in `bridge/index.ts` and `core/src/index.ts`

---

### F-04: Use BridgeError Everywhere + Standardize Error Handling

**Addresses:** Arch #3, Arch #18
**Effort:** S (Small)
**Dependencies:** F-03

**Files to modify:**
- `packages/core/src/bridge/base-api.ts` (if created) or all 4 API bridges
- `packages/core/src/bridge/base-cli.ts` (if created) or all 3 CLI bridges
- `packages/core/src/bridge/errors.ts` — add helper function
- `packages/core/src/__tests__/bridge/errors.test.ts` — add integration tests

**Steps:**
1. Add factory function in `errors.ts`: `createBridgeError(provider: string, status: number, body: string): BridgeError`
2. In `BaseAPIBridge.execute()` (or each API bridge's execute): replace `throw new Error(\`...API error (${res.status}): ${text}\`)` with `throw new BridgeError(text, res.status, this.name)`
3. In `BaseAPIBridge.streamSSE()`: on non-ok response, throw `BridgeError` instead of yielding `{ type: 'error' }`. Let `SlotWorker.stream()` catch and convert to error chunk — single place for error→chunk conversion
4. In `BaseCLIBridge.spawnAndCollect()`: throw `new BridgeError(stderr, exitCode, this.name)` on non-zero exit
5. Update `SlotWorker.execute()` retry loop: the existing `err instanceof BridgeError ? err.isRetryable() : false` check now actually works
6. Update `SlotWorker.stream()`: wrap in try/catch, convert `BridgeError` to error chunk, apply same retry logic as execute
7. Add tests: verify BridgeError is thrown with correct statusCode for 400, 429, 500 responses
8. Add tests: verify SlotWorker retries on 429/500 but not on 400/401

---

### F-05: Extract Shared GitUtils

**Addresses:** Arch #7, feeds into Gap P0-1 and P0-5
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `packages/core/src/git/utils.ts`
- `packages/core/src/git/index.ts`
- `packages/core/src/__tests__/git/utils.test.ts`

**Files to modify:**
- `packages/core/src/planner/executor.ts` — remove autoCommit, import from git/utils
- `packages/core/src/planner/ralph-loop.ts` — remove autoCommit, import from git/utils
- `packages/core/src/index.ts` — export git utilities

**Steps:**
1. Create `packages/core/src/git/utils.ts` with:
   - `autoCommit(message: string): string | undefined` — the existing logic from executor.ts lines 125-134
   - `isGitRepo(): boolean` — check if cwd is a git repo
   - `getChangedFiles(): string[]` — `git diff --name-only`
   - `stashCreate(message: string): string | undefined` — create a stash entry (for checkpoint system)
   - `stashPop(): boolean` — pop the last stash
   - `getCurrentBranch(): string`
2. Create `git/index.ts` barrel export
3. Update `PlanExecutor.autoCommit()` → call `GitUtils.autoCommit()`
4. Update `RalphLoop.autoCommit()` → call `GitUtils.autoCommit()`
5. Remove both private `autoCommit()` methods from executor.ts and ralph-loop.ts
6. Write tests for `isGitRepo()`, `autoCommit()` (mock execSync)
7. Export from `core/src/index.ts`

---

### F-06: Break Up session.tsx Into Controllers

**Addresses:** Arch #5
**Effort:** M (Medium)
**Dependencies:** None

**Files to create:**
- `packages/tui/src/controllers/slash-commands.ts`
- `packages/tui/src/controllers/plan-controller.ts`
- `packages/tui/src/controllers/loop-controller.ts`
- `packages/tui/src/controllers/system-commands.ts`

**Files to modify:**
- `packages/tui/src/routes/session.tsx` — reduce from 759 lines to ~150

**Steps:**
1. Create `slash-commands.ts`: extract `handleSlashCommand()` and all slash command handling (/help, /clear, /new, /quit, /exit, /lessons, /context, /skills, /mcp)
   - Export: `handleSlashCommand(text: string, actions: SlateActions, addSystemMessage: fn): Promise<boolean>`
2. Create `plan-controller.ts`: extract `handlePlanGenerate()`, `handlePlanExecute()`, `handlePlanReview()`, and all /plan, /plans, /resume logic
   - Export: `PlanController` class with methods for each plan operation
3. Create `loop-controller.ts`: extract `handleLoopStart()`, /loop-status, /loop-pause, /loop-resume
   - Export: `LoopController` class
4. Create `system-commands.ts`: extract /version, /update, /doctor handlers
   - Export: `handleSystemCommand(text: string, ...): Promise<boolean>`
5. Refactor `session.tsx` to import and delegate:
   - `handleSlashCommand()` delegates to slash-commands.ts first, then plan-controller, then loop-controller, then system-commands
   - session.tsx retains only: input handling, keyboard events, mode switching, layout rendering
6. Verify all slash commands still work by manual testing
7. Ensure the filtered slash command menu still works (imports from slash-menu.tsx unchanged)

---

### F-07: Dependency Injection Cleanup

**Addresses:** Arch #17
**Effort:** S (Small)
**Dependencies:** None

**Files to modify:**
- `packages/core/src/planner/executor.ts` — accept PlanStorage via constructor
- `packages/core/src/planner/ralph-loop.ts` — accept optional storage
- `packages/tui/src/routes/session.tsx` (or plan-controller.ts after F-06) — pass storage instances

**Steps:**
1. Modify `PlanExecutor` constructor: `constructor(kernel: OrchestratorKernel, storage?: PlanStorage)`
2. Default: `this.storage = storage ?? new PlanStorage()` (backward-compatible)
3. Modify `RalphLoop` constructor: `constructor(kernel: OrchestratorKernel, callbacks: LoopCallbacks, storage?: LoopStorage)`
4. Update all call sites in session.tsx (or plan-controller.ts / loop-controller.ts) to pass storage explicitly
5. This enables injecting mock storage in tests

---

### F-08: Make Hardcoded Values Configurable

**Addresses:** Arch #8
**Effort:** M (Medium)
**Dependencies:** None

**Files to modify:**
- `packages/core/src/config/schema.ts` — extend IronRainConfigSchema
- `packages/core/src/context/compaction.ts` — read config values
- `packages/core/src/context/references.ts` — read config values
- `packages/core/src/mcp/client.ts` — read timeout from config
- `packages/core/src/bridge/errors.ts` — read circuit breaker config

**Steps:**
1. Add to `IronRainConfigSchema` in `config/schema.ts`:
   ```
   context: z.object({
     hotWindowSize: z.number().default(6),
     maxContextTokens: z.number().default(8000),
     rlmRetrievalCount: z.number().default(3),
     compactionThreshold: z.number().default(8),
     maxFileSize: z.number().default(102400),      // 100KB
     maxImageSize: z.number().default(20971520),    // 20MB
   }).optional()
   mcp: z.object({
     requestTimeoutMs: z.number().default(10000),
   }).optional()
   resilience: z.object({
     circuitBreakerThreshold: z.number().default(5),
     circuitBreakerResetMs: z.number().default(60000),
     maxRetries: z.number().default(3),
   }).optional()
   ```
2. Update `compaction.ts`: `buildContextWindow()` accepts optional config param that overrides DEFAULT_COMPACTION_CONFIG
3. Update `references.ts`: accept config param for MAX_FILE_SIZE and MAX_IMAGE_SIZE
4. Update `mcp/client.ts`: accept timeout in constructor via MCPServerConfig
5. Update `bridge/errors.ts`: CircuitBreaker constructor already accepts threshold/resetMs — just wire config through
6. Thread config through from `DispatchController` → compaction/references calls
7. Thread config through from `MCPManager` → MCPClient constructor
8. Add tests for custom config values

---

### F-09: Clean Up Dead Code

**Addresses:** Arch #9, Arch #10, Arch #16
**Effort:** S (Small)
**Dependencies:** F-03 (Gemini fix depends on base class)

**Files to modify:**
- `packages/core/src/state/store.ts` — remove or move to TUI
- `packages/core/src/config/loader.ts` — replace JSONC stripper
- `packages/core/src/bridge/gemini.ts` — fix system prompt handling
- `packages/core/src/index.ts` — update exports

**Steps:**
1. **Signal class**: Check if TUI actually uses it — TUI uses SolidJS signals, not this. Remove `state/store.ts` entirely. Remove export from `core/src/index.ts`
2. **JSONC stripper**: Replace custom `stripJsoncComments()` in `config/loader.ts` with: always strip comments (not just for .jsonc extension). Use the fact that Bun's JSON parser doesn't support comments, but a simple regex-based approach is fine for the limited use case. Alternatively, use `JSON5` or `strip-json-comments` package (`bun add strip-json-comments`)
3. **Gemini system prompt**: In `gemini.ts`, replace the hacky user→"system prompt" / model→"Understood." pair with Gemini's `systemInstruction` field:
   - In `execute()`: add `system_instruction: { parts: [{ text: options.systemPrompt }] }` to request body
   - In `stream()`: same change
   - Remove the `contents.push({ role: 'user'... });` / `contents.push({ role: 'model', parts: [{ text: 'Understood.' }] });` blocks
4. Run tests to verify nothing breaks

---

### F-10: Async File Operations in references.ts

**Addresses:** Arch #11
**Effort:** S (Small)
**Dependencies:** F-02 (tests)

**Files to modify:**
- `packages/core/src/context/references.ts`

**Steps:**
1. Change `parseReferences()` to `async parseReferences()`
2. Replace `readFileSync` → `await readFile` (from `node:fs/promises`)
3. Replace `statSync` → `await stat` (from `node:fs/promises`)
4. Replace `execSync` → `await execAsync` (wrap `child_process.exec` in a promise, or use `Bun.spawn`)
5. Make `resolveFile()`, `resolveDirectory()`, `resolveImage()`, `resolveGit()` all async
6. Make `resolveToken()` async
7. Update all callers of `parseReferences()` — primarily `DispatchController.buildTask()` in `packages/tui/src/context/dispatch.ts` and the session route
8. Run reference tests to verify behavior unchanged

---

## Phase 1: Trust Layer (P0 Features)

The undo + sandbox + auto-commit + rules + session restore stack. This is what turns Iron Rain from "interesting tool" into "I can let it run."

---

### T-01: Git Checkpoint / Undo System

**Addresses:** Gap P0-1
**Effort:** M (Medium)
**Dependencies:** F-05 (GitUtils)

**Files to create:**
- `packages/core/src/git/checkpoint.ts`
- `packages/core/src/__tests__/git/checkpoint.test.ts`

**Files to modify:**
- `packages/core/src/git/utils.ts` — add checkpoint primitives
- `packages/core/src/git/index.ts` — export checkpoint
- `packages/core/src/orchestrator/kernel.ts` — add pre-dispatch checkpoint hook
- `packages/core/src/index.ts` — export CheckpointManager
- `packages/tui/src/components/slash-menu.tsx` — add /undo command
- `packages/tui/src/controllers/slash-commands.ts` (after F-06) or `packages/tui/src/routes/session.tsx` — handle /undo

**Steps:**
1. Create `checkpoint.ts` with class `CheckpointManager`:
   - `createCheckpoint(label: string): string` — runs `git stash create`, stores ref + label in an in-memory stack
   - `restoreCheckpoint(id?: string): boolean` — pops last checkpoint, runs `git checkout -- .` then `git stash pop`
   - `listCheckpoints(): Checkpoint[]` — returns stack with labels, timestamps
   - `pruneOldCheckpoints(maxAge: number): void` — remove stale checkpoints
   - Private stack: `Array<{ id: string; label: string; stashRef: string; timestamp: number; filesChanged: string[] }>`
2. Alternative approach if stash is unreliable: use lightweight commits on a shadow branch (`iron-rain/checkpoints`) with `git commit --allow-empty` and `git cherry-pick --no-commit` for restore
3. Add `checkpoint(label)` and `undo(): boolean` methods to `git/utils.ts` as convenience wrappers
4. Modify `OrchestratorKernel.dispatch()`: before dispatching tasks with toolType edit/write/bash, call `checkpointManager.createCheckpoint(task.prompt.slice(0, 80))`
5. Add `/undo` slash command: calls `checkpointManager.restoreCheckpoint()`, shows "Restored to before: {label}"
6. Add `/undo list` subcommand: shows checkpoint stack
7. Store CheckpointManager instance in SlateContext alongside MCPManager
8. Write tests: create checkpoint, make changes, restore, verify files restored

---

### T-02: Project Rules / Instructions File

**Addresses:** Gap P0-3
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `packages/core/src/rules/loader.ts`
- `packages/core/src/rules/index.ts`
- `packages/core/src/__tests__/rules/loader.test.ts`

**Files to modify:**
- `packages/core/src/orchestrator/prompts.ts` — inject rules into system prompt
- `packages/core/src/config/schema.ts` — add rules config section
- `packages/core/src/index.ts` — export rules module

**Steps:**
1. Create `rules/loader.ts` with `loadProjectRules(cwd: string): string[]` — scans for and reads: (a) `IRON-RAIN.md` at project root, (b) `CLAUDE.md` at project root (compatibility), (c) `.iron-rain/rules/*.md` sorted alphabetically, (d) `~/.iron-rain/rules/*.md` user-level global rules
2. Create `rules/index.ts` barrel export
3. Modify `buildSystemPrompt()` in `orchestrator/prompts.ts`: accept optional `rules: string[]` parameter, append as "## Project Rules" section
4. Wire rules loading in `DispatchController` constructor: call `loadProjectRules(process.cwd())` once at init, pass to `buildSystemPrompt()` on every dispatch
5. Add config in schema.ts: `rules: z.object({ paths: z.array(z.string()).optional(), disabled: z.boolean().default(false) }).optional()`
6. Support path-scoped rules: if a rules file has frontmatter `scope: src/components/**`, only inject when task references files in that scope
7. Write tests: rules loading from multiple paths, scope matching, empty case

---

### T-03: Session Persistence & Restore

**Addresses:** Gap P0-4, Arch #6 (LoopStorage)
**Effort:** M (Medium)
**Dependencies:** F-07 (DI cleanup)

**Files to create:**
- `packages/core/src/planner/loop-storage.ts`
- `packages/core/src/__tests__/planner/loop-storage.test.ts`

**Files to modify:**
- `packages/tui/src/store/session-db.ts` — add full conversation persistence helpers
- `packages/tui/src/context/slate-context.tsx` — auto-resume, session restore
- `packages/tui/src/components/slash-menu.tsx` — add /sessions command
- `packages/tui/src/controllers/slash-commands.ts` (or session.tsx) — handle /sessions, /resume

**Steps:**
1. **LoopStorage** (Arch #6): Create `loop-storage.ts` mirroring PlanStorage API: save/load/list/delete, persists to `.iron-rain/loops/{id}/loop.json`
2. Wire `LoopStorage` into `RalphLoop` via DI from F-07
3. **Session persistence**: In `session-db.ts` add `getSessionById(id)` and `getLastSession()`
4. **Auto-resume**: In `SlateProvider` on init, if `session.autoResume` config is true, call `db.getLastSession()`, restore messages, show "Resumed session" message
5. Add `/sessions` slash command listing last 10 sessions
6. Add `/resume <id>` command to restore a specific session
7. Add `/sessions clear` to delete old sessions
8. Add config: `session: z.object({ autoResume: z.boolean().default(true), maxHistory: z.number().default(50) }).optional()`
9. Write tests for LoopStorage CRUD

---

### T-04: Auto-Commit on Tool Actions

**Addresses:** Gap P0-5, uses F-05
**Effort:** S (Small)
**Dependencies:** F-05 (GitUtils), T-01 (checkpoint)

**Files to modify:**
- `packages/core/src/config/schema.ts` — add autoCommit config
- `packages/core/src/orchestrator/kernel.ts` — post-dispatch commit hook
- `packages/core/src/git/utils.ts` — add `commitIfChanged()`

**Steps:**
1. Add config: `autoCommit: z.object({ enabled: z.boolean().default(false), messagePrefix: z.string().default('iron-rain:') }).optional()`
2. Add `commitIfChanged(message: string): string | undefined` to git/utils.ts — check `git status --porcelain`, if changed run `git add -A && git commit`
3. Modify `OrchestratorKernel.dispatch()`: after successful edit/write/bash dispatch, if config.autoCommit.enabled, call `commitIfChanged()`
4. Add optional `commitHash?: string` to EpisodeSummary type, record it
5. Show commit hash in session view activity footer
6. Pairs with T-01: checkpoint before, auto-commit after

---

### T-05: .ironrainignore File Support

**Addresses:** Gap P2-20 (elevated to Phase 1 — context control is foundational)
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `packages/core/src/context/ignore.ts`
- `packages/core/src/__tests__/context/ignore.test.ts`

**Files to modify:**
- `packages/core/src/context/references.ts` — filter ignored paths
- `packages/core/src/skills/loader.ts` — respect ignore rules
- `packages/core/src/index.ts` — export ignore utilities

**Steps:**
1. Create `context/ignore.ts`: `loadIgnoreRules(cwd): IgnoreFilter` reads `.ironrainignore` (gitignore format), falls back to `.gitignore`. Uses picomatch (`bun add picomatch`) for matching. Exposes `isIgnored(path): boolean`
2. Integrate into `references.ts`: in `resolveFile()` and `resolveDirectory()`, check `ignoreFilter.isIgnored(relativePath)` before reading
3. Integrate into `skills/loader.ts`: skip skill files in ignored paths
4. Future integration point for repo map (I-02)
5. Write tests: gitignore patterns, negation, directory vs file

---

### T-06: Sandboxed Execution Phase 1 — macOS Seatbelt

**Addresses:** Gap P0-2 (Phase 1)
**Effort:** L (Large)
**Dependencies:** F-08 (configurable values)

**Files to create:**
- `packages/core/src/sandbox/index.ts`
- `packages/core/src/sandbox/types.ts`
- `packages/core/src/sandbox/seatbelt.ts`
- `packages/core/src/sandbox/profiles/default.sb`
- `packages/core/src/sandbox/profiles/permissive.sb`
- `packages/core/src/__tests__/sandbox/seatbelt.test.ts`

**Files to modify:**
- `packages/core/src/config/schema.ts` — sandbox config
- `packages/core/src/bridge/base-cli.ts` — wrap command execution
- `packages/core/src/index.ts` — export sandbox module

**Steps:**
1. Define `types.ts`: `SandboxBackend = 'none' | 'seatbelt' | 'docker'`, `SandboxConfig`, `SandboxExecutor` interface
2. Create `seatbelt.ts`: generates macOS Seatbelt profile based on config (deny network, restrict fs writes to project dir), wraps commands via `sandbox-exec -f {profile} -- {command}`
3. Add config: `sandbox: z.object({ backend: z.enum(['none','seatbelt','docker']).default('none'), allowNetwork: z.boolean().default(false), allowedWritePaths: z.array(z.string()).optional() }).optional()`
4. In `BaseCLIBridge.spawnAndCollect()`: if sandbox backend !== 'none', wrap spawn through sandbox executor
5. Auto-detect platform: only offer seatbelt on macOS
6. Add `/sandbox` slash command for status/toggle
7. Write tests: profile generation, command wrapping

---

## Phase 2: Intelligence Layer

Features that make the agent smarter and more aware.

---

### I-01: Auto-Memory / Learning

**Addresses:** Gap P1-12
**Effort:** M (Medium)
**Dependencies:** T-03 (session persistence)

**Files to create:**
- `packages/core/src/memory/auto-learner.ts`
- `packages/core/src/memory/index.ts`

**Files to modify:**
- `packages/tui/src/store/session-db.ts` — lesson storage helpers
- `packages/tui/src/context/dispatch.ts` — trigger learning
- `packages/core/src/orchestrator/prompts.ts` — inject lessons

**Steps:**
1. Create `AutoLearner` class: `summarizeSession(messages, kernel)` dispatches to main slot asking for 1-3 key learnings as JSON
2. Trigger at end of dispatch if session has 5+ messages and 10+ min since last learning
3. Store in SessionDB `lessons` table (already exists)
4. In `buildSystemPrompt()`: query last 10 lessons, include as "## Lessons Learned" section
5. Add config: `memory: z.object({ autoLearn: z.boolean().default(true), maxLessons: z.number().default(50) }).optional()`
6. Add keyword matching to prefer relevant lessons for current prompt

---

### I-02: Repository Map / Index

**Addresses:** Gap P1-9
**Effort:** M (Medium)
**Dependencies:** T-05 (.ironrainignore)

**Files to create:**
- `packages/core/src/repomap/generator.ts`
- `packages/core/src/repomap/index.ts`
- `packages/core/src/__tests__/repomap/generator.test.ts`

**Files to modify:**
- `packages/core/src/orchestrator/prompts.ts` — inject repo map
- `packages/core/src/index.ts` — export repomap

**Steps:**
1. Create `generateRepoMap(cwd, ignoreFilter): Promise<string>`: walk file tree, for each source file extract exported symbols via regex (TypeScript: `export (class|function|const|interface|type|enum) (\w+)`, Python: `^(class|def) (\w+)`)
2. Cache in memory with 60s TTL
3. Inject into system prompt as "## Repository Map" section
4. Truncate to configurable max tokens (default: 2000)
5. Add config: `repoMap: z.object({ enabled: z.boolean().default(true), maxTokens: z.number().default(2000) }).optional()`
6. Write tests for sample directory

---

### I-03: Tool Output Summarization

**Addresses:** Gap P2-14
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `packages/core/src/context/summarizer.ts`

**Files to modify:**
- `packages/core/src/orchestrator/worker.ts` — apply to large outputs
- `packages/core/src/config/schema.ts` — threshold config

**Steps:**
1. Create `summarizer.ts`: `shouldSummarize(output, threshold): boolean`, `truncateWithContext(output, maxChars): string` — keep first N and last N chars with "[...truncated...]" in middle
2. In `SlotWorker.execute()`: if result.content exceeds threshold, apply truncation
3. Add config: `context.toolOutputMaxTokens: z.number().default(2000)`
4. V1 uses simple truncation; future: use Scout slot for LLM summarization

---

### I-04: Model Cost Tracking

**Addresses:** Gap P2-19
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `packages/core/src/providers/cost-registry.ts`

**Files to modify:**
- `packages/core/src/episodes/protocol.ts` — add cost field
- `packages/core/src/orchestrator/types.ts` — add cost to WorkerResult
- `packages/tui/src/components/session-view.tsx` — display cost
- `packages/core/src/index.ts` — export cost registry

**Steps:**
1. Create `cost-registry.ts` with per-model pricing (input/output per million tokens): claude-opus-4 $15/$75, claude-sonnet-4 $3/$15, gpt-4o $2.50/$10, etc. `estimateCost(model, tokens): number`
2. Add `cost?: number` to `EpisodeSummary` and `WorkerResult`
3. In `taskToEpisode()`: calculate cost
4. Show cumulative cost in stats bar and per-task cost in SubagentCard
5. Allow custom rates in config: `costs: z.record(z.string(), z.object({ input: z.number(), output: z.number() })).optional()`

---

### I-05: Hook / Lifecycle System + Plugin Loading

**Addresses:** Gap P1-10, Arch #12
**Effort:** L (Large)
**Dependencies:** F-07

**Files to create:**
- `packages/core/src/plugins/loader.ts`
- `packages/core/src/plugins/manager.ts`
- `packages/core/src/plugins/hooks.ts`
- `packages/core/src/__tests__/plugins/manager.test.ts`

**Files to modify:**
- `packages/plugin/src/index.ts` — expand PluginHooks
- `packages/core/src/orchestrator/kernel.ts` — emit hook events
- `packages/core/src/config/schema.ts` — plugins config
- `packages/core/src/index.ts` — export plugin system

**Steps:**
1. Expand `PluginHooks`: add `onToolCall`, `onToolResult`, `onSessionStart`, `onSessionEnd`, `onCommit`, `onError`, `onCheckpoint`
2. Create `hooks.ts`: `HookEmitter` class for registered hooks
3. Create `loader.ts`: `loadPlugin(path)` — dynamic import from `.iron-rain/plugins/`
4. Create `manager.ts`: `PluginManager` with `loadAll()`, `emit(event, ...args)`, lifecycle management
5. Wire into `OrchestratorKernel`: emit lifecycle hooks
6. Support shell command hooks: config `hooks: { onCommit: "npm run lint" }`, `ShellHookRunner` executes on events
7. Add config: `plugins: z.object({ paths: z.array(z.string()).optional(), hooks: z.record(z.string()).optional() }).optional()`
8. Write tests: loading, emission, shell hooks

---

### I-06: MCP Improvements

**Addresses:** Arch #14 (tool name conflicts), Arch #15 (reconnection)
**Effort:** S (Small)
**Dependencies:** None

**Files to modify:**
- `packages/core/src/mcp/manager.ts` — conflict detection, reconnection
- `packages/core/src/mcp/client.ts` — reconnection logic

**Steps:**
1. In `getAllTools()`: detect duplicate names, namespace as `{server}.{toolName}`, log warning
2. In `callTool()`: support both plain name (first match) and namespaced
3. In `client.ts`: add `reconnect(maxAttempts, delayMs)` with exponential backoff on unexpected disconnect
4. Add `healthCheck()` method to manager
5. Add config: `mcpServers.{name}.autoReconnect: z.boolean().default(true)`

---

## Phase 3: Performance & Scale

Parallel execution, CI integration, and expanded workflows.

---

### S-01: Parallel Subagent Execution

**Addresses:** Gap P1-7
**Effort:** L (Large)
**Dependencies:** F-03 (base bridges), T-01 (checkpoints for conflict resolution)

**Files to create:**
- `packages/core/src/orchestrator/parallel.ts`
- `packages/core/src/__tests__/orchestrator/parallel.test.ts`

**Files to modify:**
- `packages/core/src/orchestrator/kernel.ts` — add `dispatchParallel()` method
- `packages/core/src/orchestrator/types.ts` — add ParallelTask type
- `packages/tui/src/components/subagent-grid.tsx` — real-time parallel status updates
- `packages/tui/src/context/dispatch.ts` — support parallel dispatch mode

**Steps:**
1. Define `ParallelTask` type: extends `OrchestratorTask` with `worktree?: string` for git worktree isolation
2. Create `parallel.ts` with `ParallelDispatcher` class:
   - `dispatch(tasks: ParallelTask[], signal?): AsyncIterable<ParallelChunk>` — runs up to N tasks concurrently (default: 4)
   - Each task gets its own SlotWorker instance
   - If tasks target different files, run in-place; if overlap, use git worktrees
3. Add `dispatchParallel(tasks, signal)` to `OrchestratorKernel`
4. Git worktree management: `git worktree add .iron-rain/worktrees/{id} HEAD`, execute in worktree, merge back with `git worktree remove`
5. Update `SubagentGrid` for real-time status: each card shows live streaming status via signal updates
6. Add `ParallelChunk` type: includes `taskIndex`, `slot`, `type`, `content`
7. Cortex can trigger parallel mode when it detects independent subtasks (via system prompt instruction)
8. Write tests: 2 parallel tasks with mocked bridges, verify both complete

---

### S-02: Headless / CI Mode Improvements

**Addresses:** Gap P1-11
**Effort:** M (Medium)
**Dependencies:** F-04 (standardized errors for exit codes)

**Files to create:**
- `packages/cli/src/headless.ts`
- `packages/cli/src/batch.ts`

**Files to modify:**
- `packages/cli/src/index.ts` — add --output, --batch, --exit-code flags
- `packages/core/src/orchestrator/kernel.ts` — add structured output mode

**Steps:**
1. Create `headless.ts`: `HeadlessRunner` class that dispatches prompts without TUI
   - Accepts `--output json` flag: emits newline-delimited JSON events: `{ type: 'text'|'error'|'done', content, slot, tokens, duration }`
   - Accepts `--output text` (default): plain text streaming to stdout
2. Create `batch.ts`: `BatchRunner` class
   - Reads prompts from a file (one per line, or JSON array)
   - Runs each sequentially (or parallel with `--parallel N`)
   - Outputs results as JSON array or individual files
3. Exit codes: 0 = success, 1 = task error, 2 = config error, 3 = provider error
4. Add `--timeout <ms>` flag for CI pipelines
5. Add `--no-streaming` flag: waits for complete response, outputs all at once
6. Integration test: run headless mode with a simple prompt, verify JSON output structure

---

### S-03: Custom Slash Commands with Arguments

**Addresses:** Gap P2-16
**Effort:** S (Small)
**Dependencies:** F-06 (session.tsx controllers)

**Files to create:**
- `packages/core/src/commands/loader.ts`
- `packages/core/src/commands/index.ts`

**Files to modify:**
- `packages/tui/src/components/slash-menu.tsx` — include custom commands
- `packages/tui/src/controllers/slash-commands.ts` — execute custom commands

**Steps:**
1. Create `commands/loader.ts`: `loadCustomCommands(cwd)` scans `.iron-rain/commands/*.md` and `~/.iron-rain/commands/*.md`
2. Each command file has YAML frontmatter: `name`, `description`, `slot` (optional). Body is a prompt template with `$ARGUMENTS` placeholder
3. When invoked (`/command-name arg1 arg2`): replace `$ARGUMENTS` with user args, dispatch to specified slot (or main)
4. Register discovered commands in slash menu alongside built-in and skill commands
5. Write tests: template expansion, argument substitution

---

### S-04: Built-in Code Review

**Addresses:** Gap P2-18
**Effort:** M (Medium)
**Dependencies:** F-05 (GitUtils)

**Files to create:**
- `packages/core/src/review/reviewer.ts`
- `packages/core/src/review/index.ts`

**Files to modify:**
- `packages/tui/src/components/slash-menu.tsx` — add /review command
- `packages/tui/src/controllers/slash-commands.ts` — handle /review

**Steps:**
1. Create `reviewer.ts` with `CodeReviewer` class:
   - `reviewStagedChanges(kernel): Promise<string>` — runs `git diff --staged`, dispatches to main slot with review prompt
   - `reviewBranch(branch, kernel): Promise<string>` — runs `git diff main..{branch}`, dispatches to main slot
   - System prompt: "You are a senior code reviewer. Analyze these changes for bugs, security issues, style problems, and missing tests."
2. `/review` — reviews staged changes
3. `/review branch <name>` — reviews diff against branch
4. Use Scout slot for line-by-line analysis if the diff exceeds 4000 tokens
5. Output formatted review with severity levels (critical/warning/suggestion)

---

### S-05: Sandboxed Execution Phase 2 — Docker

**Addresses:** Gap P0-2 (Phase 2)
**Effort:** L (Large)
**Dependencies:** T-06 (sandbox types/interface)

**Files to create:**
- `packages/core/src/sandbox/docker.ts`
- `packages/core/src/sandbox/Dockerfile`
- `packages/core/src/__tests__/sandbox/docker.test.ts`

**Files to modify:**
- `packages/core/src/sandbox/index.ts` — register docker backend
- `packages/core/src/config/schema.ts` — docker-specific config

**Steps:**
1. Create `docker.ts` implementing `SandboxExecutor`:
   - Builds/pulls a minimal sandbox image (Node.js + Bun + common tools)
   - Mounts project directory as a volume
   - Configurable network access, memory limits, CPU limits
   - `execute(command, args)` → `docker run --rm -v ... image command args`
2. Create `Dockerfile` for the sandbox image
3. Add docker-specific config: `sandbox.docker: z.object({ image: z.string().default('iron-rain-sandbox'), memoryLimit: z.string().default('2g'), cpuLimit: z.string().default('2') }).optional()`
4. Auto-detect Docker availability: `docker info` on startup
5. Write tests: verify container creation, volume mounting, cleanup

---

## Phase 4: Platform & Integration

IDE integration, language intelligence, multi-surface support, and team features.

---

### P-01: VS Code Extension

**Addresses:** Gap P1-6
**Effort:** L (Large)
**Dependencies:** S-02 (headless mode for the backend)

**Files to create:**
- `packages/vscode/` — new package
- `packages/vscode/package.json`
- `packages/vscode/src/extension.ts`
- `packages/vscode/src/terminal-panel.ts`
- `packages/vscode/src/file-links.ts`

**Steps:**
1. Create `packages/vscode/` as a new workspace package
2. Use VS Code Extension API to create a panel that embeds the Iron Rain TUI in an integrated terminal
3. `terminal-panel.ts`: manages the terminal instance, restarts on crash
4. `file-links.ts`: intercepts file paths in assistant responses, makes them clickable to open in editor
5. Add commands: "Iron Rain: Open", "Iron Rain: New Session", "Iron Rain: Send Selection"
6. "Send Selection" command: sends selected code as context to Iron Rain
7. Publish as a separate `@howlerops/iron-rain-vscode` package

---

### P-02: LSP Integration

**Addresses:** Gap P1-8
**Effort:** L (Large)
**Dependencies:** I-02 (repo map)

**Files to create:**
- `packages/core/src/lsp/client.ts`
- `packages/core/src/lsp/diagnostics.ts`
- `packages/core/src/lsp/index.ts`

**Files to modify:**
- `packages/core/src/orchestrator/prompts.ts` — inject diagnostics
- `packages/core/src/config/schema.ts` — LSP config

**Steps:**
1. Create `lsp/client.ts`: `LSPClient` class that connects to workspace language servers via stdio
2. Auto-detect: look for `tsconfig.json` → TypeScript LS, `pyproject.toml` → Python LS, etc.
3. Create `diagnostics.ts`: after each edit/write dispatch, request diagnostics from LSP, inject errors into next prompt for auto-fix
4. Add to repo map: use LSP `textDocument/documentSymbol` for richer symbol extraction
5. Add config: `lsp: z.object({ enabled: z.boolean().default(false), servers: z.record(z.string(), z.object({ command: z.string(), args: z.array(z.string()) })).optional() }).optional()`

---

### P-03: Remote Config / Team Sharing

**Addresses:** Gap P2-17
**Effort:** S (Small)
**Dependencies:** F-08 (config system)

**Files to modify:**
- `packages/core/src/config/schema.ts` — add configUrl field
- `packages/core/src/config/loader.ts` — fetch remote config

**Steps:**
1. Add `configUrl: z.string().url().optional()` to schema
2. In `loadConfig()`: if local config has `configUrl`, fetch remote JSON, deep-merge with local (local wins on conflicts)
3. Cache remote config for 1 hour to avoid network dependency
4. Support `.well-known/iron-rain.json` convention for org-wide defaults
5. Add `/config sync` slash command to force re-fetch

---

### P-04: Multi-Surface Support / Web UI

**Addresses:** Gap P2-13
**Effort:** XL (Extra Large)
**Dependencies:** S-02 (headless mode), I-05 (hook system)

**Files to create:**
- `packages/server/` — new package (HTTP + WebSocket server)
- `packages/web/` — new package (web UI)

**Steps:**
1. Create `packages/server/`: HTTP server that wraps OrchestratorKernel
   - REST endpoints: POST /dispatch, GET /sessions, GET /status
   - WebSocket for streaming responses
   - Authentication via API token
2. Create `packages/web/`: lightweight web UI
   - Use the same component structure as TUI but rendered in browser
   - Framework: Solid Start or vanilla SolidJS + Vite
3. `iron-rain serve` command starts the server
4. `iron-rain attach <url>` connects TUI to remote server
5. This is a multi-sprint effort — start with server package and basic web rendering

---

### P-05: Voice Mode

**Addresses:** Gap P2-15
**Effort:** S (Small)
**Dependencies:** None

**Files to create:**
- `packages/core/src/input/voice.ts`

**Files to modify:**
- `packages/tui/src/routes/session.tsx` — voice input trigger

**Steps:**
1. Create `voice.ts`: use macOS `say`/`SFSpeechRecognizer` or cross-platform Web Speech API equivalent
2. For v1: shell out to `rec` (SoX) for recording + `whisper.cpp` for local transcription
3. Add `/voice` slash command to toggle voice input mode
4. Transcribed text feeds into the same dispatch pipeline as typed input
5. Low priority — quick win for accessibility

---

### P-06: Sandboxed Execution Phase 3 — Configurable Backends

**Addresses:** Gap P0-2 (Phase 3)
**Effort:** M (Medium)
**Dependencies:** T-06 (seatbelt), S-05 (docker)

**Files to create:**
- `packages/core/src/sandbox/gvisor.ts`
- `packages/core/src/sandbox/lxc.ts`

**Files to modify:**
- `packages/core/src/sandbox/index.ts` — registry pattern for backends
- `packages/core/src/config/schema.ts` — expand backend enum

**Steps:**
1. Refactor `sandbox/index.ts` into a registry: `SandboxRegistry.register(name, factory)`
2. Add `gvisor.ts`: Linux-only, uses `runsc` for OCI container sandboxing
3. Add `lxc.ts`: Linux-only, uses LXC/LXD for container isolation
4. Expand config backend enum: `'none' | 'seatbelt' | 'docker' | 'gvisor' | 'lxc'`
5. Auto-detect available backends on startup, recommend the best one for the platform

---

## Cross-Reference Matrix

Every original item mapped to its plan task:

| Original Item | Plan Task | Phase |
|---------------|-----------|-------|
| Arch #1: Test framework | F-02 | 0 |
| Arch #2: Biome linting | F-01 | 0 |
| Arch #3: BridgeError usage | F-04 | 0 |
| Arch #4: Base bridge classes | F-03 | 0 |
| Arch #5: Break up session.tsx | F-06 | 0 |
| Arch #6: LoopStorage | T-03 | 1 |
| Arch #7: Extract autoCommit | F-05 | 0 |
| Arch #8: Configurable values | F-08 | 0 |
| Arch #9: Remove unused Signal | F-09 | 0 |
| Arch #10: JSONC stripper | F-09 | 0 |
| Arch #11: Async file ops | F-10 | 0 |
| Arch #12: Plugin loading | I-05 | 2 |
| Arch #13: Anthropic model discovery | F-08 | 0 |
| Arch #14: MCP tool conflicts | I-06 | 2 |
| Arch #15: MCP reconnection | I-06 | 2 |
| Arch #16: Gemini system prompt | F-09 | 0 |
| Arch #17: DI cleanup | F-07 | 0 |
| Arch #18: Error handling | F-04 | 0 |
| Gap P0-1: Git undo | T-01 | 1 |
| Gap P0-2: Sandbox (Seatbelt) | T-06 | 1 |
| Gap P0-2: Sandbox (Docker) | S-05 | 3 |
| Gap P0-2: Sandbox (Backends) | P-06 | 4 |
| Gap P0-3: Project rules | T-02 | 1 |
| Gap P0-4: Session persistence | T-03 | 1 |
| Gap P0-5: Auto-commit | T-04 | 1 |
| Gap P1-6: VS Code extension | P-01 | 4 |
| Gap P1-7: Parallel subagents | S-01 | 3 |
| Gap P1-8: LSP integration | P-02 | 4 |
| Gap P1-9: Repo map | I-02 | 2 |
| Gap P1-10: Hook system | I-05 | 2 |
| Gap P1-11: Headless/CI | S-02 | 3 |
| Gap P1-12: Auto-memory | I-01 | 2 |
| Gap P2-13: Web UI | P-04 | 4 |
| Gap P2-14: Tool summarization | I-03 | 2 |
| Gap P2-15: Voice mode | P-05 | 4 |
| Gap P2-16: Custom commands | S-03 | 3 |
| Gap P2-17: Remote config | P-03 | 4 |
| Gap P2-18: Code review | S-04 | 3 |
| Gap P2-19: Cost tracking | I-04 | 2 |
| Gap P2-20: .ironrainignore | T-05 | 1 |

---

## Dependency Graph

```
Phase 0: Foundation
═══════════════════
F-01 (Biome)           ── no deps ──────────────────────────────┐
F-02 (Tests)           ── no deps ──────────────────────────────┤
F-05 (GitUtils)        ── no deps ──────────────────────────────┤
F-06 (Session split)   ── no deps ──────────────────────────────┤
F-07 (DI cleanup)      ── no deps ──────────────────────────────┤
F-08 (Config values)   ── no deps ──────────────────────────────┤
F-10 (Async refs)      ── F-02 ────────────────────────────────┤
F-03 (Base bridges)    ── F-02 ────────────────────────────────┤
F-04 (BridgeError)     ── F-03 ────────────────────────────────┤
F-09 (Dead code)       ── F-03 ────────────────────────────────┤
                                                                │
Phase 1: Trust Layer                                            │
════════════════════                                            │
T-02 (Project rules)   ── no deps ─────────────────────────────┤
T-05 (.ironrainignore) ── no deps ─────────────────────────────┤
T-01 (Git undo)        ── F-05 ────────────────────────────────┤
T-03 (Sessions)        ── F-07 ────────────────────────────────┤
T-04 (Auto-commit)     ── F-05, T-01 ─────────────────────────┤
T-06 (Seatbelt)        ── F-08 ────────────────────────────────┤
                                                                │
Phase 2: Intelligence                                           │
═════════════════════                                           │
I-03 (Summarization)   ── no deps ─────────────────────────────┤
I-04 (Cost tracking)   ── no deps ─────────────────────────────┤
I-06 (MCP fixes)       ── no deps ─────────────────────────────┤
I-01 (Auto-memory)     ── T-03 ────────────────────────────────┤
I-02 (Repo map)        ── T-05 ────────────────────────────────┤
I-05 (Hooks/plugins)   ── F-07 ────────────────────────────────┤
                                                                │
Phase 3: Performance                                            │
════════════════════                                            │
S-02 (Headless/CI)     ── F-04 ────────────────────────────────┤
S-03 (Custom cmds)     ── F-06 ────────────────────────────────┤
S-04 (Code review)     ── F-05 ────────────────────────────────┤
S-01 (Parallel)        ── F-03, T-01 ─────────────────────────┤
S-05 (Docker sandbox)  ── T-06 ────────────────────────────────┤
                                                                │
Phase 4: Platform                                               │
═════════════════                                               │
P-03 (Remote config)   ── F-08 ────────────────────────────────┤
P-05 (Voice)           ── no deps ─────────────────────────────┤
P-01 (VS Code)         ── S-02 ────────────────────────────────┤
P-02 (LSP)             ── I-02 ────────────────────────────────┤
P-04 (Web UI)          ── S-02, I-05 ─────────────────────────┤
P-06 (Sandbox backends)── T-06, S-05 ──────────────────────────┘
```

---

## Effort Summary

| Phase | Tasks | Small | Medium | Large | XL |
|-------|-------|-------|--------|-------|----|
| 0: Foundation | 10 | 6 | 4 | 0 | 0 |
| 1: Trust Layer | 6 | 3 | 2 | 1 | 0 |
| 2: Intelligence | 6 | 3 | 2 | 1 | 0 |
| 3: Performance | 5 | 1 | 2 | 2 | 0 |
| 4: Platform | 6 | 2 | 1 | 2 | 1 |
| **Total** | **33** | **15** | **11** | **6** | **1** |

**Estimated timeline** (1 engineer):
- Phase 0: 2-3 weeks
- Phase 1: 2-3 weeks
- Phase 2: 2-3 weeks
- Phase 3: 3-4 weeks
- Phase 4: 6-8 weeks (VS Code + Web UI are the big ones)