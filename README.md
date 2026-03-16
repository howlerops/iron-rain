# Iron Rain

Multi-model orchestration for terminal-based coding agents.

```
   ____                  ____        _
  /  _/______  ___      / __ \___ _ (_)___
 _/ / / __/ _ \/ _ \   / /_/ / _  |/ / _ \
/___/ /_/  \___/_//_/  / .___/\_,_/_/_//_/
                      /_/
```

Route tasks to the right model at the right time. Use any provider — Anthropic, OpenAI, Ollama, or any OpenAI-compatible API.

## Quick Start

```bash
git clone https://github.com/howlerops/iron-rain
cd iron-rain
bun install
bun run build
```

### Run headless

```bash
node packages/cli/dist/index.js --headless "Explain this codebase"
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

## Architecture

Iron Rain uses a three-slot model orchestration system:

| Slot | Purpose | Routes |
|------|---------|--------|
| **Main** | Strategy, planning, conversation | `strategy`, `plan`, `conversation` |
| **Explore** | Search, read, research | `grep`, `glob`, `read`, `search` |
| **Execute** | Edit, write, run commands | `edit`, `write`, `bash` |

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

## Development

```bash
bun install
bun run build       # Build all packages
bun run typecheck   # Type check all packages
```

## License

MIT — see [LICENSE](LICENSE).

Part of the [howlerops](https://github.com/howlerops) ecosystem.
