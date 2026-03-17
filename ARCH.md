# Iron Rain — Architecture Document

## Executive Summary

Iron Rain is an open-source multi-model orchestration system for terminal-based coding agents. It routes tasks to the right model at the right time across any provider — Anthropic, OpenAI, Ollama, Gemini, or CLI-based tools like Claude Code, Codex, and Gemini CLI.

The project is a **Bun + Turborepo monorepo** with 4 packages, ~4,500 lines of TypeScript across 65+ source files, zero test coverage, and zero linting infrastructure.

---

## Monorepo Structure

```
iron-rain/
├── packages/
│   ├── core/          @howlerops/iron-rain          Core library (zero UI deps)
│   ├── tui/           @howlerops/iron-rain-tui      Terminal UI (SolidJS + OpenTUI)
│   ├── cli/           @howlerops/iron-rain-cli      CLI entry point
│   └── plugin/        @howlerops/iron-rain-plugin   Plugin SDK
├── agents/            Agent profile definitions (build, explore, plan)
├── docs/              Static HTML documentation site
├── scripts/           Installation scripts
├── .github/           CI/CD workflows (ci, publish, release)
├── .claude/           Claude Code skills + agents (howler-agents system)
├── iron-rain.json     Default slot configuration
├── turbo.json         Build pipeline
├── tsconfig.json      Shared TypeScript config
└── package.json       Root workspace config (Bun v1.3.6)
```

### Package Dependency Graph

```
                ┌──────────┐
                │   cli    │
                └────┬─────┘
                     │ depends on
              ┌──────┴──────┐
              ▼             ▼
        ┌──────────┐  ┌──────────┐
        │   tui    │  │  plugin  │
        └────┬─────┘  └────┬─────┘
             │              │
             └──────┬───────┘
                    ▼
              ┌──────────┐
              │   core   │
              └──────────┘
```

All packages are MIT-licensed, published to npm under `@howlerops/` scope, version 0.1.6.

---

## Build System

| Tool | Version | Purpose |
|------|---------|---------|
| Bun | 1.3.6 | Package manager + runtime |
| Turborepo | 2.4.0 | Monorepo build orchestration |
| TypeScript | 5.7+ | Type checking + compilation |

### Build Pipeline (turbo.json)

| Task | Depends On | Outputs | Cached |
|------|-----------|---------|--------|
| `build` | `^build` | `dist/**` | Yes |
| `dev` | — | — | No (persistent) |
| `typecheck` | `^build` | — | Yes |
| `clean` | — | — | No |
| `lint` | — | — | Yes (no-op) |

### Per-Package Build

| Package | Build Command | Notes |
|---------|--------------|-------|
| core | `tsc` | Standard TypeScript compilation |
| cli | `tsc` | Standard TypeScript compilation |
| plugin | `tsc` | Standard TypeScript compilation |
| tui | `bun run build.ts` | Custom: SolidJS plugin + tsc declarations |

---

## Architecture Layers

```
┌─────────────────────────────────────────────────────────────────┐
│  CLI / TUI                        User Interface Layer          │
│  @howlerops/iron-rain-cli                                       │
│  @howlerops/iron-rain-tui (SolidJS + OpenTUI)                   │
├─────────────────────────────────────────────────────────────────┤
│  Orchestrator                     Dispatch + Routing Layer      │
│  OrchestratorKernel                                             │
│  ├── ModelSlotManager                                           │
│  ├── ToolRouter (heuristic pattern matching)                    │
│  └── SlotWorker (per slot, with circuit breaker + fallback)     │
├─────────────────────────────────────────────────────────────────┤
│  Context Layer                    Prompt Engineering             │
│  ├── @ References (file, dir, git, image injection)             │
│  ├── RLM Compaction (hot/cold window + keyword retrieval)       │
│  └── Episode Context (prior action summaries)                   │
├─────────────────────────────────────────────────────────────────┤
│  Planner Layer                    Task Orchestration             │
│  ├── PlanGenerator (PRD → task breakdown via Cortex)            │
│  ├── PlanExecutor (sequential task dispatch via Forge)           │
│  ├── RalphLoop (iterative execution until condition met)         │
│  └── SkillExecutor (markdown skill injection)                   │
├─────────────────────────────────────────────────────────────────┤
│  Bridge Layer                     Provider Abstraction           │
│  ├── API: Anthropic, OpenAI-compat, Ollama, Gemini              │
│  └── CLI: ClaudeCode, Codex, GeminiCLI                          │
├─────────────────────────────────────────────────────────────────┤
│  Integration Layer                External Systems               │
│  ├── MCP (JSON-RPC 2.0 stdio transport)                        │
│  ├── Skills (markdown file discovery + registry)                │
│  └── Sessions (SQLite persistence via bun:sqlite)               │
└─────────────────────────────────────────────────────────────────┘
```

---

## Core Package Deep Dive

### Three-Slot Model System

The defining feature. Three named slots, each assignable to any provider + model:

| Slot | Agent Name | Purpose | Routed Tool Types |
|------|-----------|---------|-------------------|
| **main** | Cortex | Strategy, planning, conversation | strategy, plan, conversation |
| **explore** | Scout | Search, read, research | grep, glob, read, search |
| **execute** | Forge | Edit, write, run commands | edit, write, bash |

**Type definitions** (`slots/types.ts`):
- `SlotName`: `'main' | 'explore' | 'execute'`
- `ToolType`: 10 tool types mapped to slots
- `SlotConfig`: provider, model, apiKey, apiBase, thinkingLevel, systemPrompt, fallback
- `ThinkingLevel`: `'off' | 'low' | 'medium' | 'high'`

### Task Dispatch Flow

```
User Prompt
    │
    ▼
DispatchController.dispatch()
    │
    ├── parseReferences()          Resolve @file, @dir, @git, @image
    ├── buildContextWindow()       RLM compaction (hot/cold split)
    ├── detectToolType()           Heuristic regex matching
    ├── buildSystemPrompt()        Slot-specific persona + context
    │
    ▼
OrchestratorKernel.dispatchStreaming()
    │
    ├── Slot Resolution:
    │   1. task.targetSlot (explicit)
    │   2. getSlotForTool(task.toolType) (routed)
    │   3. 'main' (fallback)
    │
    ▼
SlotWorker.stream()
    │
    ├── Circuit breaker check
    ├── Bridge.stream() ──► Provider API/CLI
    ├── On failure: retry with backoff
    └── On circuit open: fallback bridge
    │
    ▼
Streaming chunks → UI
    │
    ▼
EpisodeSummary recorded
```

### Bridge System (`bridge/`)

7 bridge implementations sharing the `CLIBridge` interface:

```typescript
interface CLIBridge {
  name: string;
  available(): Promise<boolean>;
  execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult>;
  stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk>;
}
```

| Bridge | Type | Provider | Lines |
|--------|------|----------|-------|
| AnthropicBridge | API | Anthropic Messages API | 149 |
| OpenAICompatBridge | API | Any OpenAI-compatible endpoint | 160 |
| OllamaBridge | API | Local Ollama server | 146 |
| GeminiBridge | API | Google Generative Language API | 168 |
| ClaudeCodeBridge | CLI | `claude -p` subprocess | 188 |
| CodexBridge | CLI | `codex exec` subprocess | 78 |
| GeminiCLIBridge | CLI | `gemini -p` subprocess | 124 |

**Factory**: `createBridgeForSlot()` in `bridge/index.ts` resolves provider string → bridge class.

### Resilience Infrastructure (`bridge/errors.ts`)

| Component | Purpose | Status |
|-----------|---------|--------|
| `BridgeError` | Error with statusCode + provider + isRetryable() | **Defined but unused** |
| `CircuitBreaker` | Opens after 5 failures, auto-resets after 60s | **Used by SlotWorker** |
| `RetryConfig` | maxRetries: 3, baseDelay: 1s, maxDelay: 30s | **Used by SlotWorker** |
| `backoffDelay()` | Exponential backoff with jitter | **Used by SlotWorker** |

### Context Management

**RLM Compaction** (`context/compaction.ts`):
- Hot window: last 6 messages kept verbatim
- Cold archive: older messages compacted into summaries
- Keyword-based retrieval pulls relevant archived messages back
- Token estimation: ~4 chars/token (rough heuristic)

**@ References** (`context/references.ts`):
- `@./path` or `@file:path` → file contents (100KB max)
- `@dir:path` → directory listing
- `@git:cmd` → whitelisted git commands (diff, status, log, branch, stash)
- `@image:path` → base64-encoded image (20MB max)
- `@cortex/@scout/@forge` → slot routing prefix

### Planner System (`planner/`)

Two execution models:

**Plan & Execute** (PlanGenerator + PlanExecutor):
1. `/plan "description"` → Cortex generates PRD via main slot
2. Cortex breaks PRD into JSON task array
3. User reviews (approve/reject/edit)
4. PlanExecutor dispatches tasks sequentially to execute slot
5. Auto-commits after each task (optional)
6. Plans persisted to `.iron-rain/plans/<id>/`

**Ralph Wiggum Loop** (RalphLoop):
1. `/loop "task" --until "condition"`
2. Each iteration dispatched to execute slot
3. Completion checked via LLM call to main slot (TRUE/FALSE)
4. After 3+ iterations, prompts for different strategy
5. **Not persisted** — in-memory only

### Skills System (`skills/`)

- Discovers markdown files from 4 paths: project/user `.iron-rain/skills/` + `.claude/skills/`
- Parses YAML frontmatter for metadata (name, description, command)
- Skills inject their markdown body as `systemPrompt` on the main slot
- Skills become slash commands in the TUI

### MCP Integration (`mcp/`)

- JSON-RPC 2.0 over stdio transport
- `MCPClient`: spawns server process, handles request/response with 10s timeout
- `MCPManager`: manages multiple servers, routes tool calls by name
- Tool descriptions injected into system prompts

### Provider Registries (`providers/`)

- `ProviderRegistry`: static registry of known providers + models
- `ModelRegistry`: dynamic model fetching for Ollama, OpenAI, Gemini with 5-min cache

### Plugin SDK (`plugin/`)

Lifecycle hooks for extending Iron Rain:

```typescript
interface PluginHooks {
  onInit?(ctx: PluginContext): void | Promise<void>;
  onBeforeDispatch?(task: OrchestratorTask, ctx: PluginContext): OrchestratorTask | void;
  onAfterDispatch?(episode: EpisodeSummary, ctx: PluginContext): void;
  onSlotChange?(slot: SlotName, config: SlotConfig, ctx: PluginContext): void;
  onDestroy?(ctx: PluginContext): void | Promise<void>;
}
```

---

## TUI Package Deep Dive

### Technology Stack

| Layer | Technology |
|-------|-----------|
| Rendering | OpenTUI (`@opentui/core`, `@opentui/solid`) |
| Reactivity | SolidJS 1.9.9 |
| Persistence | SQLite via `bun:sqlite` |
| Runtime | Bun (required for TUI mode; Node.js for headless) |

### State Management

**Central Provider Pattern**: `SlateProvider` → `SlateContext` → `[SlateState, SlateActions]`

- `createStore`: complex nested state (messages, slots, stats, contextDirs)
- `createSignal`: simple reactive values (isLoading, activeSlot, streamingContent)
- `DispatchController`: extracted orchestration logic with streaming + abort
- `SessionDB`: SQLite persistence with `NullSessionDB` fallback

### Component Architecture

```
App
├── OnboardingWizard (setup flow)
│   ├── Welcome
│   ├── ProviderSelect
│   ├── Credentials
│   ├── SlotAssignment
│   └── Summary
└── SessionRoute (main interaction)
    ├── SessionView (message list + streaming)
    │   ├── UserMessage
    │   ├── AssistantMessage
    │   │   └── SubagentGrid (multi-agent cards)
    │   ├── StreamingMessage
    │   └── CumulativeStats
    ├── SlashMenu (command autocomplete)
    ├── Settings (model + provider config)
    ├── PlanView / PlanReview
    ├── SkillPicker
    └── UpdateBanner
```

### Session Database Schema

```sql
sessions (id, created_at, updated_at, model)
messages (id, session_id, role, content, slot, timestamp, tokens, duration, sort_order)
activities (id, message_id, slot, task, status, duration, tokens)
lessons (id, content, source, created_at, expires_at, tags)
```

---

## CI/CD

| Workflow | Trigger | What It Does |
|----------|---------|-------------|
| `ci.yml` | Push/PR to main | Typecheck + build |
| `publish.yml` | Tag push (`v*`) | Publish 4 npm packages with provenance |
| `release.yml` | Tag push (`v*`) | Create GitHub release with auto-notes |

---

## Module Dependency Graph (Core)

```
Layer 0 (no deps):     utils/id, state/store
Layer 1:               slots/types
Layer 2:               slots/defaults, episodes/protocol
Layer 3:               router/tool-router, bridge/types, bridge/errors, bridge/thinking
Layer 4:               slots/slot-manager, config/schema
Layer 5:               config/loader, bridge/* (all implementations), providers/*
Layer 6:               bridge/index (factory)
Layer 7:               orchestrator/types, orchestrator/prompts
Layer 8:               orchestrator/worker
Layer 9:               orchestrator/kernel
Layer 10:              context/*, mcp/*, skills/*, planner/*
```

**No circular dependencies detected.** All imports flow strictly downward.

---

## Configuration

### iron-rain.json Schema

```json
{
  "slots": {
    "main":    { "provider": "...", "model": "...", "fallback": {...} },
    "explore": { "provider": "...", "model": "..." },
    "execute": { "provider": "...", "model": "..." }
  },
  "providers": { "<name>": { "apiKey": "env:VAR", "apiBase": "..." } },
  "permission": { "*": "allow|deny|ask", "bash": "ask" },
  "agent": "build",
  "lcm": { "enabled": true, "episodes": { "maxEpisodeTokens": 4000 } },
  "theme": "default",
  "updates": { "autoCheck": true, "channel": "stable|beta|canary" },
  "mcpServers": { "<name>": { "command": "...", "args": [], "env": {} } },
  "skills": { "paths": ["..."], "autoDiscover": true }
}
```

Config supports `.json` and `.jsonc` (with custom comment stripping). Walks up directory tree to find config. API keys can use `env:` prefix for environment variable resolution.

---

## Key Design Decisions

1. **Three-slot architecture** over single-model — enables cost optimization (cheap models for search, expensive for code)
2. **Bridge abstraction** over SDK dependency — uniform interface for API and CLI providers
3. **SolidJS + OpenTUI** over React + Ink — native terminal rendering via bun:ffi
4. **Episode summaries** over full message passing — compressed context sharing between slots
5. **RLM compaction** over sliding window — keyword retrieval preserves relevant older context
6. **File-based plan storage** over database — human-readable PRDs alongside JSON state
7. **Plugin hooks** over middleware chain — simpler lifecycle model for extensions
```