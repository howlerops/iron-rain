# Oculus claude-code sidecar

Drives a **persistent, streaming** claude-code session via the [Claude Agent SDK](https://github.com/anthropics/claude-agent-sdk-typescript)
and bridges it to the Go daemon over stdio (line-delimited JSON). It replaces the old
single-shot `claude -p` provider, whose `PreToolUse` hook does **not** block in `-p` mode
(anthropics/claude-code#36071) — meaning tools could run *unapproved*. Here the SDK's
`canUseTool` callback genuinely blocks the tool until the daemon answers.

## Install
```sh
cd daemon/agent/claudecode/sidecar
npm install        # or: bun install
```

## Auth — uses your claude subscription (verified live)
The sidecar spawns your **logged-in `claude` CLI**, so it uses your **claude.ai
subscription** — no API key required. Verified live: `TestLive_RealClaudeCode` passes
with `ANTHROPIC_API_KEY` unset. Just make sure `claude` is logged in (`claude` once,
or check `~/.claude/.credentials.json`).

To use a metered API key instead (e.g. headless/CI where no login exists), set
`ANTHROPIC_API_KEY=sk-ant-...`.

## Run (the daemon does this for you)
```sh
oculusd serve --claude-sidecar $(pwd)/sidecar.mjs   # spawns: node sidecar.mjs
```
The daemon sets `OCULUS_SESSION_ID` + `OCULUS_MODE` (create|attach) and speaks the
stdio protocol documented at the top of `../claudecode.go`.

## Protocol
| dir | frame |
|---|---|
| daemon→sidecar | `{"t":"prompt","text"}` · `{"t":"approval","id","decision":"allow\|deny"}` · `{"t":"stop"}` |
| sidecar→daemon | `{"t":"session","id"}` · `{"t":"text","text"}` · `{"t":"thinking","text"}` · `{"t":"tool","tool","detail"}` · `{"t":"approval","id","tool","detail"}` · `{"t":"idle"}` · `{"t":"error","message"}` |

Streaming deltas use the SDK's `includePartialMessages` (`stream_event` → `content_block_delta`
with `text_delta`/`thinking_delta`); approvals use `permissionMode: "default"` + `canUseTool`.

## Test
- **Go side (offline, no SDK/LLM):** `cd daemon && go test ./agent/claudecode/` drives the
  provider against a fake sidecar shell script — proves the streaming + blocking-approval contract.
- **Live (opt-in, spends tokens):** `OCULUS_CLAUDE_SIDECAR=$(pwd)/sidecar.mjs go test
  ./agent/claudecode/ -run TestLive_RealClaudeCode -v` (needs `ANTHROPIC_API_KEY` + `npm install`).
