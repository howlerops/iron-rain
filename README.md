# Iron Rain

Multi-model orchestration for terminal-based coding agents.

```
╭━━╮╭━━━╮╭━━━╮╭━╮╱╭╮   ╭━━━╮╭━━━╮╭━━╮╭━╮╱╭╮
╰┫┣╯┃╭━╮┃┃╭━╮┃┃┃╰╮┃┃   ┃╭━╮┃┃╭━╮┃╰┫┣╯┃┃╰╮┃┃
╱┃┃╱┃╰━╯┃┃┃╱┃┃┃╭╮╰╯┃   ┃╰━╯┃┃┃╱┃┃╱┃┃╱┃╭╮╰╯┃
╱┃┃╱┃╭╮╭╯┃┃╱┃┃┃┃╰╮┃┃   ┃╭╮╭╯┃╰━╯┃╱┃┃╱┃┃╰╮┃┃
╭┫┣╮┃┃┃╰╮┃╰━╯┃┃┃╱┃┃┃   ┃┃┃╰╮┃╭━╮┃╭┫┣╮┃┃╱┃┃┃
╰━━╯╰╯╰━╯╰━━━╯╰╯╱╰━╯   ╰╯╰━╯╰╯╱╰╯╰━━╯╰╯╱╰━╯
```

Route tasks to the right model at the right time. Use any provider — Anthropic, OpenAI, Ollama, or any OpenAI-compatible API.

## Quick Start

```bash
# One-line install
curl -fsSL https://raw.githubusercontent.com/howlerops/iron-rain/main/scripts/install.sh | bash

# Or install manually
npm install -g @howlerops/iron-rain-cli

# Launch the TUI
iron-rain
```

### Run headless

```bash
iron-rain --headless "Explain this codebase"
```

### Commands

```
iron-rain                    Launch TUI
iron-rain --headless "task"  Run without TUI
iron-rain config             Show current config
iron-rain models             List available models
iron-rain --version          Show version
iron-rain --help             Show help
```

### TUI Slash Commands

```
/init                        Analyze project structure + architecture review
/plan <desc>                 Generate PRD + task breakdown
/plans                       List saved plans
/resume                      Resume paused plan
/loop <desc> --until "cond"  Iterative execution loop
/loop-status                 Show loop progress
/loop-pause / /loop-resume   Control active loop
/review [branch]             Code review (staged or branch diff)
/undo                        Restore last checkpoint
/context add|list|remove     Manage context directories
/lessons                     Show persistent memory
/skills                      List available skills
/mcp                         Show MCP server status
/model                       Show model assignments
/slot [name]                 Show or set active slot
/stats                       Show session statistics
/settings                    Open settings
/help                        Show all commands
/version                     Show version info
/update                      Check for updates
/doctor                      Run diagnostics
/clear                       Clear session
/new                         New session
/quit or /exit               Exit
```

## Architecture

Iron Rain uses a three-slot model orchestration system:

| Slot | Agent | Purpose | Routes |
|------|-------|---------|--------|
| **main** | Cortex | Strategy, planning, conversation | `strategy`, `plan`, `conversation` |
| **explore** | Scout | Search, read, research | `grep`, `glob`, `read`, `search` |
| **execute** | Forge | Edit, write, run commands | `edit`, `write`, `bash` |

Each slot can be assigned to any model from any provider. The orchestrator automatically routes tool calls to the appropriate slot.

## Configuration

Create an `iron-rain.json` in your project root:

```json
{
  "slots": {
    "main":    { "provider": "anthropic", "model": "claude-opus-4-6" },
    "explore": { "provider": "ollama",    "model": "qwen2.5-coder:32b" },
    "execute": { "provider": "openai",    "model": "gpt-4o" }
  },
  "providers": {
    "anthropic": { "apiKey": "env:ANTHROPIC_API_KEY" },
    "openai":    { "apiKey": "env:OPENAI_API_KEY" },
    "ollama":    { "apiBase": "http://localhost:11434" }
  }
}
```

API keys can reference environment variables with the `env:` prefix.

## Packages

| Package | Description |
|---------|-------------|
| `@howlerops/iron-rain` | Model slots, orchestrator, bridges, config — zero UI deps |
| `@howlerops/iron-rain-tui` | Terminal UI components (SolidJS + OpenTUI) |
| `@howlerops/iron-rain-cli` | CLI entry point |
| `@howlerops/iron-rain-plugin` | Plugin SDK for extending Iron Rain |

## Providers

Built-in bridge support for:

### API Bridges (direct API calls)
- **Anthropic** — Claude models via Messages API
- **OpenAI** — GPT models and any OpenAI-compatible API
- **Ollama** — Local models via Ollama API
- **Gemini** — Google Gemini models via Generative Language API

### CLI Bridges (use your existing subscriptions)
- **Claude Code** — Uses your Claude Pro/Max subscription via the `claude` CLI
- **Codex** — Uses your OpenAI subscription via the `codex` CLI
- **Gemini CLI** — Uses your Google subscription via the `gemini` CLI

### Example: Mix API + CLI providers

```json
{
  "slots": {
    "main":    { "provider": "claude-code", "model": "opus" },
    "explore": { "provider": "gemini-cli",  "model": "gemini-2.5-flash" },
    "execute": { "provider": "codex",       "model": "o3" }
  }
}
```

## Plan & Execute

Generate a PRD and task breakdown, review it, then execute automatically:

```
/plan Add user authentication with JWT tokens
```

Cortex generates a PRD and task list. Review with `approve`, `reject`, or `edit <feedback>`. On approval, Forge executes tasks sequentially with auto-commit support. Plans are saved to `.iron-rain/plans/` and can be resumed with `/resume`.

## Iterative Loop

Run a task repeatedly until a condition is met:

```
/loop Fix all failing tests --until "ALL TESTS PASSING"
```

Each iteration sees prior results. If stuck for 3+ iterations, the loop auto-suggests a different strategy. Control with `/loop-pause` and `/loop-resume`.

## Skills

Iron Rain discovers markdown skill files from `.iron-rain/skills/`, `~/.iron-rain/skills/`, and `.claude/skills/`. Skills become slash commands:

```
/skills              # List all skills
/<skill-name> [args] # Execute a skill
```

## Lessons (Persistent Memory)

Cross-session memory stored in SQLite (`~/.iron-rain/sessions.db`):

```
/lessons             # Show saved lessons
```

Lessons survive session resets and can be injected into prompts for continuity.

## @ Context References

Inject files, directories, git state, or images into any prompt:

```
@./src/index.ts explain this file
@git:diff what changed?
@dir:src/components/ what's the structure?
@./screenshot.png implement this design
```

| Syntax | Resolves to |
|--------|-------------|
| `@./path` or `@file:path` | File contents (100KB max) |
| `@dir:path` | Directory listing |
| `@git:diff\|status\|log\|branch\|stash` | Git command output |
| `@./image.png` or `@image:path` | Base64 image for multimodal models |

Multiple references can be combined in a single message. Slot routing (`@cortex`, `@scout`, `@forge`) is preserved — bare words at the start route to slots, while path-style references inject context.

## Context Directories

Add external directories to your session scope with the `/context` command:

```
/context add ../other-project
/context list
/context remove ../other-project
```

Files in added directories can then be referenced with `@` mentions.

## Project Init

Analyze your codebase in one command:

```
/init
```

Maps your repo structure, reads package metadata, detects config files, and dispatches an architecture review. Findings are stored as persistent lessons that survive across sessions.

## Code Review

Review staged changes or branch diffs:

```
/review          # Review staged changes
/review main     # Review diff against main
```

Gets structured feedback on bugs, security, performance, and style.

## Checkpoints & Undo

Git-based checkpoint system for safe plan execution:

```
/undo            # Restore last checkpoint
```

Each plan task creates a checkpoint before execution. Restore any time with `/undo`.

## Project Rules

Define coding standards that are injected into every system prompt:

- `IRON-RAIN.md` — project root rules file
- `CLAUDE.md` — also supported (Claude Code compatibility)
- `.iron-rain/rules/*.md` — additional rule files

Disable with `"rules": { "disabled": true }` in config.

## Repo Map

Auto-generated repository map injected into system prompts. Walks the directory tree (respecting `.ironrainignore` and `.gitignore`), extracts symbols (functions, classes, exports), and truncates to a configurable token budget.

Configure in `iron-rain.json`:

```json
{
  "repoMap": { "enabled": true, "maxTokens": 2000 }
}
```

## Cost Tracking

Per-model cost registry tracks input/output token pricing across all slots:

```json
{
  "costs": {
    "custom-model": { "input": 0.001, "output": 0.002 }
  }
}
```

Built-in models have default pricing. Override or add custom models in config.

## Sandbox Execution

Run commands in isolated sandboxes:

```json
{
  "sandbox": {
    "backend": "docker",
    "allowNetwork": false,
    "docker": { "image": "node:20-slim", "memoryLimit": "2g" }
  }
}
```

Backends: `none` (default), `seatbelt` (macOS), `docker`, `gvisor`.

## Mid-Stream Context Injection

While the agent is streaming, type additional context and press **Enter** to inject it. The response pauses, your text is added to the history, and the agent resumes with the updated context. Press **Esc** to cancel the stream entirely.

## Remote Config

Fetch and merge team-wide config at startup:

```json
{
  "configUrl": "https://example.com/team-iron-rain.json"
}
```

Local values take precedence over remote.

## Fuzzy Command Suggestions

Mistype a command? Iron Rain suggests the closest match:

```
> /upgade
Unknown command: /upgade. Did you mean /update? (Check for and install updates)
```

## Development

```bash
bun install
bun run build       # Build all packages
bun run typecheck   # Type check all packages
bun test            # Run all tests
```

## License

MIT — see [LICENSE](LICENSE).

Part of the [howlerops](https://github.com/howlerops) ecosystem.
