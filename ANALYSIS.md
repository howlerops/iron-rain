# @randomlabs/slate - Comprehensive Architecture Analysis

## Executive Summary

**Slate is a proprietary fork/rebrand of [OpenCode](https://github.com/anomalyco/opencode) (MIT License, 123k+ GitHub stars)** with added proprietary features around multi-model orchestration ("Thread Weaving"), dynamic context pruning, and a swarm-native subagent system.

The evidence is definitive:
- `~/.slate/package.json` lists `"@opencode-ai/plugin": "1.0.16"` as a dependency
- Every `SLATE_*` environment variable maps 1:1 to an `OPENCODE_*` equivalent
- The binary contains identical tool names, config structures, and architecture patterns
- The shell integration scripts are adapted from VS Code's terminal integration (same as OpenCode)

**Implication for open-slate**: We don't need to reverse-engineer Slate. We can build directly on OpenCode (MIT licensed) and add the multi-model orchestration features that make Slate unique.

---

## 1. Package Structure

### @randomlabs/slate (v1.0.16)
```
package/
├── bin/
│   ├── slate          # Node.js launcher script (CJS)
│   └── slate1         # Bun launcher script (ESM)
├── package.json       # Wrapper with platform-specific optional deps
├── postinstall.mjs    # Verifies platform binary exists
└── LICENSE            # "Proprietary - Copyright (c) 2026 Random Labs"
```

### Platform Binaries (e.g., @randomlabs/slate-darwin-arm64)
```
package/
├── bin/
│   └── slate          # 81.4 MB Mach-O arm64 binary (Bun-compiled)
└── package.json
```

The thin wrapper `bin/slate` resolves the correct platform binary from `node_modules/@randomlabs/slate-{platform}-{arch}/bin/slate` and executes it via `child_process.spawnSync`.

### Older @randomlabs/slatecli (v0.0.32)
The previous version reveals the full tech stack before binary compilation:
```
package/
├── dist/
│   ├── index.js           # 11.8 MB esbuild + obfuscated bundle
│   ├── package.json       # Dev dependency manifest (reveals all packages)
│   ├── scripts/           # Shell integration (bash, zsh, fish, powershell)
│   └── vendor/
│       └── ripgrep/       # Bundled rg binaries per platform
├── scripts/
│   └── postinstall.bundled.js
└── package.json
```

---

## 2. Technology Stack

### Core (from slatecli dev dependencies + binary analysis)

| Layer | Technology | Purpose |
|-------|-----------|---------|
| **Runtime** | Bun (v1.0 compiled binary) | JS runtime + bundler |
| **TUI Framework** | React 19 + Ink 6 | Terminal UI rendering |
| **Language** | TypeScript (esbuild bundled) | Type-safe development |
| **Database** | better-sqlite3 | Session/chat persistence |
| **Code Search** | Ripgrep (bundled) | Fast file content search |
| **Terminal** | node-pty (prebuilt) | PTY for shell execution |
| **Validation** | Zod | Schema validation |
| **CLI Parsing** | Yargs | Argument parsing |
| **LSP** | vscode-jsonrpc + vscode-languageserver-types | Code intelligence |
| **Logging** | Winston | Structured logging |
| **Analytics** | PostHog | Telemetry (SLATE_TELEMETRY_DISABLED) |
| **Security** | @napi-rs/keyring | Secure API key storage |
| **Image** | Sharp (optional) | Screenshot processing |
| **Diffing** | diff | Code diff generation |
| **Syntax** | highlight.js + lowlight | Terminal syntax highlighting |
| **Serialization** | protobufjs | Efficient data serialization |
| **Obfuscation** | javascript-obfuscator | Source code protection |

### AI/LLM Layer

| Component | Technology |
|-----------|-----------|
| **Agent Framework** | @entropy-research/agent (private, v1.0.86) |
| **Plugin System** | @opencode-ai/plugin (MIT, v1.0.16) |
| **AI SDK Patterns** | Vercel AI SDK compatible (AI_* error types) |
| **Anthropic** | @anthropic-ai/sdk (ChatAnthropic) |
| **OpenAI** | ChatOpenAI, AzureOpenAI |
| **Google** | ChatGoogleGenerativeAI |
| **Groq** | ChatGroq |
| **Mistral** | ChatMistralAI |
| **DeepSeek** | deepseek- models |
| **Z-AI** | z-ai/glm-5 (GLM models) |

### TUI Components (Ink ecosystem)
- `ink-big-text` - Large text rendering
- `ink-gradient` - Gradient text effects
- `ink-link` - Clickable terminal links
- `ink-select-input` - Selection menus
- `ink-spinner` - Loading indicators

---

## 3. Architecture Deep Dive

### 3.1 Three-Model Slot System

Slate's most distinctive feature vs OpenCode. From the onboarding config:

```json
{
  "selectedMainModel": "anthropic/claude-opus-4.6",
  "selectedExploreModel": "z-ai/glm-5",
  "selectedExecuteModel": "openai/gpt-5.3-codex"
}
```

| Slot | Purpose | Default Model |
|------|---------|---------------|
| **Main** | Orchestration, conversation, strategy | Claude Opus 4.6 |
| **Explore** | Codebase search, documentation research | GLM-5 |
| **Execute** | Code execution, tactical operations | GPT-5.3 Codex |

Binary evidence: `availableModelSlots`, `cycleModelSlot`, `cycleFavoriteModelSlot`, `cycleRecentModelSlot`, `getModelSlot`, `listModelSlots`, `closeModelSlotPicker`, `currentSelectedModelIndex`.

### 3.2 Thread Weaving (Orchestration Architecture)

Marketing name for Slate's multi-model parallel execution system:

```
┌─────────────────────────────────┐
│     Central Orchestration       │
│     Thread ("Kernel")           │
│  - Programs in "action space"   │
│  - Manages execution graph      │
│  - Dispatches to worker threads │
│  - Integrates episode summaries │
└──────────┬──────────────────────┘
           │
     ┌─────┼─────┬──────────┐
     ▼     ▼     ▼          ▼
┌────────┐┌────────┐┌────────┐┌────────┐
│Worker 1││Worker 2││Worker 3││Worker N│
│(Edit)  ││(Search)││(Bash)  ││(...)   │
│Claude  ││GLM-5   ││GPT-5.3 ││Model X │
└────────┘└────────┘└────────┘└────────┘
```

Binary evidence: `orchestrate`, `Orchestrated`, `OrchestrateToolWrapper`, `Orchestrating`, `orchestration_started`, `orchestrateTimestamp`, `no-orchestrate`.

**Key concepts:**
- **Episodes**: Compressed summaries returned from worker threads (not verbose transcripts)
- **Action Space Programming**: Orchestrator generates high-level actions, not direct code
- **Direct Context Sharing**: Episodes share context directly with orchestrator (not message passing)
- **Knowledge Overhang**: Separating strategy from tactics lets models focus intelligence

### 3.3 Subagent System

```
Binary evidence:
- addSubagentUsageToCurrentTurn
- captureAgentResponse
- completedSubagentDiffs
- completedSubagents
- currentTurnAgentCount
- currentTurnSubagentSessionIds
- handleSubagents
- interruptedSubagents
- getSubagentInvocationSegment
- /subagents (API endpoint)
- ChatPage-SubagentFocus
- subagent-header
```

Subagents are spawned as separate sessions with their own model assignment, tool access, and permissions. They return diffs and results to the parent agent.

### 3.4 Tool System

Confirmed tool types from binary:
- `isCompletionTool` - Signals task completion
- `isEditTool` - File modification tools
- `isSearchTool` - Code search tools
- `isTerminalTool` - Shell execution tools
- `isPathBasedTool` - File-path-aware tools
- `isEscalateTool` - Permission escalation
- `isFailureTool` - Error signaling
- `isFileWriteTool` - File creation tools

Specific tools: `Bash`, `Edit`, `Glob`, `Search`, `file_read`, `batch_tool`, `bash tool`.

Tool management: `callTool`, `callToolStream`, `availableTools`, `enabledTools`, `captureToolPermission`, `getToolOutputValidator`, `cacheToolMetadata`, `criticaltools`.

### 3.5 Permission System

```
Environment: SLATE_PERMISSION, SLATE_DANGEROUS_SKIP_PERMISSIONS
Config: allowedPermissions, captureToolPermission
Modes: allow, ask, deny (per tool, per agent)
```

Granular bash command permissions with glob patterns (inherited from OpenCode).

### 3.6 Configuration System

| File | Location | Purpose |
|------|----------|---------|
| `slate.json` / `slate.jsonc` | Project root | Project-specific config |
| `slate.local` | Project root | Local overrides (gitignored) |
| `.slate/` | Project root | Project slate directory |
| `.slate/agents/` | Project root | Custom agent definitions |
| `.slate/commands/` | Project root | Custom commands |
| `~/.slate/` | Home directory | Global config/state |
| `AGENTS.md` | Project root | Agent instructions (like CLAUDE.md) |

### 3.7 MCP (Model Context Protocol)

Binary evidence: `slate-mcp-client`, `/mcp`, `/mcp/oauth/callback`, `getServerTools`, `allServers`, `addServer`, `createServer`, `_serverCapabilities`, `_serverInfo`, `_serverParams`.

Full MCP client support for connecting to external tool servers.

### 3.8 Session Management

- SQLite-backed persistent sessions
- Git worktree integration (separate sessions per branch)
- Session fork/continue/export/import
- `createSession`, `getAllSessions`, `getChildSessions`, `getSessionCommands`, `getSessionMetadata`

### 3.9 Shell Integration

Adapted from VS Code's terminal shell integration. Uses OSC 633 escape sequences for:
- Command detection (prompt start/end, command output)
- Working directory tracking
- Environment variable reporting
- Rich command detection

Scripts for: bash, zsh, fish, PowerShell.

---

## 4. Environment Variables

Complete list extracted from binary:

| Variable | Purpose |
|----------|---------|
| `SLATE_API_KEY` | API key for Slate services |
| `SLATE_AUTO_SHARE` | Auto-share sessions |
| `SLATE_CLIENT` | Client identifier |
| `SLATE_CONFIG` | Config file path |
| `SLATE_CONFIG_CONTENT` | Inline JSON config |
| `SLATE_CONFIG_DIR` | Config directory path |
| `SLATE_DIR` | Slate data directory |
| `SLATE_TOKEN_DIR` | Token storage directory |
| `SLATE_AGENT` | Default agent name |
| `SLATE_PERMISSION` | Permission overrides (JSON) |
| `SLATE_DANGEROUS_SKIP_PERMISSIONS` | Bypass all permissions |
| `SLATE_DISABLE_AUTOUPDATE` | Skip update checks |
| `SLATE_DISABLE_CLAUDE_CODE` | Disable Claude Code integration |
| `SLATE_DISABLE_CLAUDE_CODE_PROMPT` | Disable CC prompt injection |
| `SLATE_DISABLE_CLAUDE_CODE_SKILLS` | Disable CC skills |
| `SLATE_DISABLE_DEFAULT_PLUGINS` | Skip default plugins |
| `SLATE_DISABLE_FILETIME_CHECK` | Disable file timestamp checks |
| `SLATE_DISABLE_LSP_DOWNLOAD` | Don't auto-download LSP servers |
| `SLATE_DISABLE_MODELS_FETCH` | Don't fetch model list |
| `SLATE_DISABLE_PROJECT_CONFIG` | Ignore project configs |
| `SLATE_DISABLE_PRUNE` | Disable context pruning |
| `SLATE_DISABLE_TERMINAL_TITLE` | Don't update terminal title |
| `SLATE_ENABLE_EXA` | Enable Exa search integration |
| `SLATE_ENABLE_EXPERIMENTAL_MODELS` | Show experimental models |
| `SLATE_EXPERIMENTAL` | Enable experimental features |
| `SLATE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS` | Bash timeout |
| `SLATE_EXPERIMENTAL_DISABLE_COPY_ON_SELECT` | Disable copy behavior |
| `SLATE_EXPERIMENTAL_DISABLE_FILEWATCHER` | Disable file watching |
| `SLATE_EXPERIMENTAL_FILEWATCHER` | Enable file watcher |
| `SLATE_EXPERIMENTAL_ICON_DISCOVERY` | Icon discovery feature |
| `SLATE_EXPERIMENTAL_LSP_TOOL` | LSP as a tool |
| `SLATE_EXPERIMENTAL_LSP_TY` | LSP type inference |
| `SLATE_EXPERIMENTAL_MARKDOWN` | Markdown rendering |
| `SLATE_EXPERIMENTAL_OUTPUT_TOKEN_MAX` | Max output tokens |
| `SLATE_EXPERIMENTAL_OXFMT` | Ox formatter |
| `SLATE_EXPERIMENTAL_PLAN_MODE` | Enable plan mode |
| `SLATE_FAKE_VCS` | Fake VCS for testing |
| `SLATE_GIT_BASH_PATH` | Path to git bash |
| `SLATE_MODELS_URL` | Custom models endpoint |
| `SLATE_SERVER_PASSWORD` | Server auth password |
| `SLATE_SERVER_USERNAME` | Server auth username |
| `SLATE_TELEMETRY_DISABLED` | Disable analytics |
| `SLATE_TEST_HOME` | Test home directory |

---

## 5. Slate vs OpenCode: What's Added

| Feature | OpenCode (MIT) | Slate (Proprietary) |
|---------|---------------|-------------------|
| TUI Framework | React + Ink | React + Ink (same) |
| Single Model | Yes | Yes |
| Multi-Model Slots | No (single model per agent) | Yes (Main/Explore/Execute) |
| Thread Weaving | No | Yes (orchestration layer) |
| Dynamic Pruning | Auto-compact (basic) | Advanced pruning algo |
| Subagents | Yes (general, explore) | Enhanced with orchestration |
| MCP | Yes | Yes |
| Providers | 7+ providers | 7+ providers + Z-AI |
| Permission System | Yes | Yes (same) |
| Git Worktree | Yes | Yes |
| LSP | Yes | Yes |
| Plugin System | Yes (@opencode-ai/plugin) | Uses OpenCode plugins |
| Shell Integration | VS Code adapted | VS Code adapted (same) |
| Claude Code Integration | No | Yes (SLATE_DISABLE_CLAUDE_CODE) |
| Exa Search | No | Yes (SLATE_ENABLE_EXA) |
| Client/Server | Yes (serve/attach) | Likely yes (same base) |

---

## 6. Strategy for open-slate

### Recommended Approach

**Don't rebuild from scratch. Fork/extend OpenCode.**

OpenCode is MIT licensed, has 123k+ stars, 818 contributors, and covers 90% of what Slate offers. The key additions we'd need to build:

#### Must Build (Slate's proprietary additions):
1. **Multi-Model Slot System** - Allow assigning different models to different roles (orchestrate/explore/execute)
2. **Thread Weaving Orchestrator** - Central orchestration thread that dispatches work to model-specific workers
3. **Episode Compression** - Structured summaries from worker threads (not verbose transcripts)
4. **Dynamic Context Pruning** - Active context management beyond simple auto-compact
5. **Cross-Model Context Sharing** - Direct episode sharing between heterogeneous models

#### Already Available in OpenCode:
- Multi-provider support (Anthropic, OpenAI, Google, Groq, etc.)
- Agent/subagent system with model assignment
- Tool system (Bash, Edit, Search, Glob, etc.)
- Permission system with granular controls
- MCP client support
- Git worktree integration
- LSP integration
- Plugin system
- Session management with SQLite
- Shell integration
- Client/server architecture

### Architecture for open-slate

```
open-slate/
├── packages/
│   ├── core/              # Fork of OpenCode core
│   ├── orchestrator/      # NEW: Thread Weaving implementation
│   │   ├── kernel.ts      # Central orchestration thread
│   │   ├── worker.ts      # Worker thread management
│   │   ├── episode.ts     # Episode compression/sharing
│   │   └── router.ts      # Model selection/routing
│   ├── model-slots/       # NEW: Multi-model slot system
│   │   ├── slot.ts        # Slot definition and management
│   │   ├── selector.ts    # Model picker UI
│   │   └── config.ts      # Slot configuration
│   ├── pruning/           # NEW: Dynamic context pruning
│   │   ├── pruner.ts      # Active pruning algorithm
│   │   ├── relevance.ts   # Context relevance scoring
│   │   └── budget.ts      # Token budget management
│   ├── providers/         # Extended provider support
│   │   ├── ollama.ts      # Local model support
│   │   ├── lmstudio.ts    # LM Studio integration
│   │   └── custom.ts      # Custom endpoint support
│   ├── tui/               # Terminal UI (React + Ink)
│   └── plugins/           # Plugin system
├── agents/                # Agent definitions
├── docs/                  # Documentation
└── opencode.json          # Compatible config format
```

### Key Design Decisions

1. **Stay compatible with OpenCode ecosystem** - Use the same config format, plugin system, and agent definitions
2. **Provider-agnostic from day one** - Support Ollama, LM Studio, and any OpenAI-compatible API
3. **CLI-agnostic orchestration** - The Thread Weaving layer should be able to orchestrate Claude Code, Codex, or any CLI tool as a "worker"
4. **Open protocol for episodes** - Define a standard format for episode compression that any tool can implement
5. **Pluggable pruning** - Allow custom pruning strategies via the plugin system

---

## 7. recursive-llm-ts: What We Already Have

The [recursive-llm-ts](https://github.com/howlerops/recursive-llm-ts) package (MIT, v5.0.2) already implements many of the core backend concepts that power Slate's Thread Weaving and dynamic pruning. This is our primary advantage — we have production-ready implementations of the hardest parts.

### 7.1 Direct Concept Mapping

| Slate Concept | recursive-llm-ts Implementation | Status |
|--------------|-------------------------------|--------|
| **Episode Compression** | `lcm_episodes.go` — EpisodeManager with auto-rotation, compaction, budget-based retrieval | **Done** |
| **Dynamic Pruning** | `lcm_context_loop.go` — Dual-threshold (τ_soft/τ_hard) context control loop | **Done** |
| **5-Level Summarization** | `lcm_summarizer.go` — LLM normal → LLM aggressive → TF-IDF → TextRank → deterministic truncation | **Done** |
| **Immutable Context Store** | `lcm_store.go` — Immutable message store + hierarchical Summary DAG with provenance | **Done** |
| **Worker Thread Spawning** | `lcm_agentic_map.go` — Full sub-agent RLM sessions per item, configurable concurrency | **Done** |
| **Parallel Batch Processing** | `lcm_map.go` — LLM-Map with 16-worker pool, schema validation, retry | **Done** |
| **Delegation Guard** | `lcm_delegation.go` — Scope-reduction invariant, infinite recursion prevention | **Done** |
| **Parallel Task Decomposition** | `DelegateTasks()` — Goroutine-based parallel sub-agent execution | **Done** |
| **Token Management** | `tokenizer.go` — BPE (tiktoken), cached counting, model-specific encodings | **Done** |
| **Context Overflow Recovery** | `context_overflow.go` — 6 strategies: mapreduce, truncate, chunked, tfidf, textrank, refine | **Done** |
| **Observability** | `observability.go` — OpenTelemetry tracing, Langfuse integration, debug logging | **Done** |
| **SQLite Persistence** | `store_sqlite.go` — WAL mode, FTS5 full-text search, transactional writes | **Done** |
| **Meta-Agent Query Optimization** | `meta_agent.go` — Analyze and rewrite vague queries | **Done** |
| **Multi-Model Slot System** | — | **Not Built** |
| **TUI (React + Ink)** | — | **Not Built** (OpenCode provides this) |
| **Tool System (Bash/Edit/Glob)** | — | **Not Built** (OpenCode provides this) |
| **Shell Integration** | — | **Not Built** (OpenCode provides this) |
| **MCP Client** | — | **Not Built** (OpenCode provides this) |
| **Permission System** | — | **Not Built** (OpenCode provides this) |

### 7.2 Architecture Comparison

```
┌─────────────────────────────────────────────────────────────────┐
│                    SLATE ARCHITECTURE                            │
│                                                                 │
│  ┌─────────────┐  ┌──────────────┐  ┌─────────────────────┐   │
│  │ React + Ink  │  │ OpenCode     │  │ @entropy-research/  │   │
│  │ TUI          │  │ Plugin Sys   │  │ agent (private)     │   │
│  └──────┬───────┘  └──────┬───────┘  └──────────┬──────────┘   │
│         │                 │                      │              │
│  ┌──────▼─────────────────▼──────────────────────▼──────────┐  │
│  │              Thread Weaving Orchestrator                   │  │
│  │  - Model Slots (Main/Explore/Execute)                     │  │
│  │  - Episode compression from workers                       │  │
│  │  - Dynamic pruning algorithm                              │  │
│  │  - Action space programming                               │  │
│  └──────────────────────┬────────────────────────────────────┘  │
│                         │                                       │
│  ┌──────────────────────▼────────────────────────────────────┐  │
│  │              Provider Layer (Vercel AI SDK patterns)       │  │
│  │  ChatAnthropic | ChatOpenAI | ChatGroq | ChatMistral      │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│              RECURSIVE-LLM-TS ARCHITECTURE                      │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │           TypeScript API Layer (src/)                     │   │
│  │  RLM Class | Builder | Events | Cache | Retry | Stream   │   │
│  │  Coordinator | FileStorage | Config | Errors             │   │
│  └──────────────────────┬──────────────────────────────────┘   │
│                         │ JSON via stdin/stdout                  │
│  ┌──────────────────────▼──────────────────────────────────┐   │
│  │           Go Binary Engine (go/rlm/)                      │   │
│  │                                                           │   │
│  │  ┌─── LCM Layer ──────────────────────────────────────┐  │   │
│  │  │ Immutable Store + Summary DAG (lcm_store.go)       │  │   │
│  │  │ 5-Level Summarization (lcm_summarizer.go)          │  │   │
│  │  │ Context Control Loop (lcm_context_loop.go)         │  │   │
│  │  │ Episodic Memory (lcm_episodes.go)                  │  │   │
│  │  │ Delegation Guard (lcm_delegation.go)               │  │   │
│  │  │ LLM-Map / Agentic-Map (lcm_map.go, lcm_agentic_*) │  │   │
│  │  │ File Handling (lcm_files.go)                       │  │   │
│  │  │ SQLite Backend (store_sqlite.go, FTS5)             │  │   │
│  │  └────────────────────────────────────────────────────┘  │   │
│  │                                                           │   │
│  │  ┌─── Core Layer ─────────────────────────────────────┐  │   │
│  │  │ RLM Engine + REPL (rlm.go, repl.go)               │  │   │
│  │  │ Structured Output (structured.go, schema.go)       │  │   │
│  │  │ Meta-Agent (meta_agent.go)                         │  │   │
│  │  │ Context Overflow (context_overflow.go, 6 strategies)│  │   │
│  │  │ TF-IDF + TextRank (tfidf.go, textrank.go)         │  │   │
│  │  │ BPE Tokenizer (tokenizer.go, tiktoken)             │  │   │
│  │  │ OpenAI-Compatible Client (openai.go)               │  │   │
│  │  │ Observability (observability.go, OTEL + Langfuse)  │  │   │
│  │  └────────────────────────────────────────────────────┘  │   │
│  └───────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

### 7.3 What recursive-llm-ts Provides That Slate Needs

**Episodic Memory** (`lcm_episodes.go`)
- Episode lifecycle: active → compacted → archived
- Auto-rotation at token/message limits (configurable, defaults: 2000 tokens, 20 messages)
- Parent chaining for conversation continuity
- Budget-based retrieval: selects episodes fitting token budget
- Active episode always in context; compacted episodes use summary tokens
- **Maps to**: Slate's episode compression in Thread Weaving

**Context Control Loop** (`lcm_context_loop.go`)
- Dual-threshold: τ_soft (70%) for async compaction, τ_hard (90%) for blocking compaction
- Zero-cost below soft threshold — no overhead added
- **Maps to**: Slate's `SLATE_DISABLE_PRUNE` / dynamic pruning algorithm

**Five-Level Summarization** (`lcm_summarizer.go`)
- Level 1: LLM summarize (preserve details)
- Level 2: LLM bullet points (half target tokens)
- Level 3: TF-IDF extractive (no LLM, preserves sentences)
- Level 4: TextRank graph-based (no LLM, better coherence)
- Level 5: Deterministic truncate (guaranteed convergence)
- **Maps to**: Slate's context compression. Slate likely uses similar escalation.

**Delegation System** (`lcm_delegation.go`)
- Scope-reduction invariant prevents infinite recursion
- Sub-agents must declare `delegated_scope` and `kept_work`
- Detects trivial delegation (e.g., "waiting", "collect results")
- Parallel decomposition exempt from guard
- `DelegateTasks()` for goroutine-based parallel sub-agent execution
- **Maps to**: Slate's subagent spawning and the `handleSubagents` system

**Agentic-Map** (`lcm_agentic_map.go`)
- Full sub-agent RLM sessions per item
- Configurable concurrency (default: 8), max depth (3), max iterations (15)
- Read-only flag for exploration agents
- Output validation with schema
- **Maps to**: Slate's worker threads in Thread Weaving

**Immutable Store + Summary DAG** (`lcm_store.go`)
- Every message persisted verbatim, never modified
- Hierarchical summaries with provenance (message_ids, child_ids, parent_id)
- File IDs propagate through DAG during compaction
- Active context assembled from recent messages + summary pointers
- **Maps to**: Slate's session management and context reconstruction

**SQLite Backend** (`store_sqlite.go`)
- Pure-Go SQLite (no CGO), WAL mode
- FTS5 full-text search for message content
- Transactional writes for crash recovery
- **Maps to**: Slate's better-sqlite3 session storage

### 7.4 Benchmarks (Already Proven)

From recursive-llm-ts context savings tests (reproducible, no LLM calls):

| Pipeline | Input | Output | Savings |
|----------|-------|--------|---------|
| TF-IDF compression (5K→2K) | 4,930 tokens | 1,989 tokens | 59.7% |
| TextRank compression (5K→2K) | 4,930 tokens | 1,972 tokens | 60.0% |
| Episodic memory (50 msgs, budget 200) | 5,500 tokens | 550 tokens | 90.0% |
| Combined pipeline (100 msgs) | 50,491 tokens | 7,669 tokens | 84.8% |

### 7.5 Revised Strategy: Three-Layer Integration

Given that recursive-llm-ts already implements the backend context management, the open-slate architecture should be:

```
Layer 1: OpenCode (MIT)
  └── TUI, Tools, Permissions, MCP, Git, LSP, Sessions, Agents, Shell Integration

Layer 2: recursive-llm-ts / Go Engine (MIT)
  └── LCM, Episodes, Summarization, Delegation, Token Management, Observability

Layer 3: open-slate (NEW — what we build)
  └── Multi-Model Slots, Thread Weaving Orchestrator, Model Router, CLI Bridges
```

#### What We Actually Need to Build

1. **Model Slot Manager** — Assign models to roles (Main/Explore/Execute), cycle UI, persistence
2. **Thread Weaving Orchestrator** — Dispatch tasks to the right model slot, collect episode summaries
3. **Model Router** — Determine which slot handles which tool call (Edit→Execute, Search→Explore, strategy→Main)
4. **CLI Bridge Layer** — Wrap Claude Code, Codex, Ollama, etc. as "workers" that the orchestrator can dispatch to
5. **Episode Protocol** — Standard format for episode compression between orchestrator and workers
6. **OpenCode Integration Plugin** — Wire the recursive-llm-ts Go engine into OpenCode's compaction system

#### What We Do NOT Need to Build (already exists)

| Component | Source |
|-----------|--------|
| Terminal UI | OpenCode (React + Ink) |
| Tool system (Bash, Edit, Glob, Search) | OpenCode |
| Permission system | OpenCode |
| MCP client | OpenCode |
| Git worktree integration | OpenCode |
| LSP integration | OpenCode |
| Plugin system | OpenCode (@opencode-ai/plugin) |
| Session management | OpenCode (SQLite) |
| Shell integration | OpenCode (bash, zsh, fish, PowerShell) |
| Client/server architecture | OpenCode (serve/attach) |
| Episodic memory | recursive-llm-ts (lcm_episodes.go) |
| Context control loop | recursive-llm-ts (lcm_context_loop.go) |
| 5-level summarization | recursive-llm-ts (lcm_summarizer.go) |
| Delegation guard | recursive-llm-ts (lcm_delegation.go) |
| Parallel task execution | recursive-llm-ts (DelegateTasks, AgenticMap) |
| Token management (BPE) | recursive-llm-ts (tokenizer.go) |
| Context overflow recovery | recursive-llm-ts (6 strategies) |
| Immutable store + DAG | recursive-llm-ts (lcm_store.go) |
| SQLite persistence + FTS5 | recursive-llm-ts (store_sqlite.go) |
| Observability (OTEL/Langfuse) | recursive-llm-ts (observability.go) |

---

## 8. Sources

- [OpenCode GitHub (anomalyco/opencode)](https://github.com/anomalyco/opencode) - 123k stars, MIT License
- [OpenCode Documentation](https://opencode.ai/docs/)
- [OpenCode Agent System](https://opencode.ai/docs/agents/)
- [OpenCode CLI Reference](https://opencode.ai/docs/cli/)
- [Random Labs Website](https://randomlabs.ai/)
- [VentureBeat: Slate V1 Launch](https://venturebeat.com/orchestration/y-combinator-backed-random-labs-launches-slate-v1-claiming-the-first-swarm)
- [Random Labs Blog: Beyond ReAct and RLM](https://randomlabs.ai/blog/slate)
- [Y Combinator: Random Labs](https://www.ycombinator.com/companies/random-labs)
- [npm: @randomlabs/slate](https://www.npmjs.com/package/@randomlabs/slate)
- [npm: opencode-ai](https://www.npmjs.com/package/opencode-ai)
- [npm: @opencode-ai/plugin](https://www.npmjs.com/package/@opencode-ai/plugin)
- [recursive-llm-ts GitHub](https://github.com/howlerops/recursive-llm-ts) - MIT License
- [RLM Paper (Alex Zhang, Omar Khattab)](https://alexzhang13.github.io/blog/2025/rlm/)
- [LCM Paper (Voltropy, 2026)](https://papers.voltropy.com/LCM)
