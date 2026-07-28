# Iron Rain

**A native Apple Agent Development Environment.** Run real coding agents on your own Mac, then
launch, watch, steer, and **approve** them from your Mac or your iPhone — from anywhere. End-to-end
encrypted, your hardware, your keys.

> [howlerops.github.io/iron-rain](https://howlerops.github.io/iron-rain/)

Iron Rain runs the agents you already pay for (claude-code, opencode, and more) as processes on your
machine, and gives them a real Apple front end: a native SwiftUI app for macOS and iOS that talks to
a Go daemon over an encrypted WebSocket protocol. Close the laptop, keep shipping.

## Why

Terminal agents live on one machine, behind one keyboard. The moment you walk away, you're blind.
Web dashboards fix the "anywhere" part by shipping your code and your keys to someone else's servers.

Iron Rain keeps everything on **your** hardware. The agents run locally against **your**
subscriptions. Remote access goes through a relay that only ever sees ciphertext — it can forward
your traffic but it can't read it. When an agent needs sign-off on a tool call, the request lands on
your iPhone lock screen and you approve or deny it with a tap. No analytics, no tracking, no code
leaving your box.

## What it does

**Agents & chat**
- Multi-provider: `opencode` (HTTP/SSE), `claude-code` (Agent SDK sidecar), `pi` (JSONL), plus custom
  CLI agents (codex, gemini, aider, …). Provider auto-detect, per-session model pick and switch.
- Native composer with slash-command completion, per-session drafts, and code-safe input (no
  smart-quotes). Streaming markdown + thinking, rich inline tool cards (command + expandable output),
  and collapsible sub-agents that stream their own work.
- **Generative UI** (`iron:ui`): agents render native tables, checklists, callouts, diffs, and
  tappable choices inline — across every harness, down to a plain CLI.

**Remote & approvals**
- Remote from anywhere, E2E encrypted (X25519 / ChaCha20-Poly1305). A stateless relay forwards only
  ciphertext; the app races your LAN and the relays. APNs push.
- Approve or deny a tool call straight from the iPhone lock screen. Spending guardrails and cost
  budgets keep autonomous runs in check.

**Code & tickets**
- Native syntax-highlighted editor, file tree, and LSP (diagnostics, hover, go-to-definition,
  completion, rename), scoped to the session's project. A native diff review you **comment on** to
  steer the agent.
- Full two-way Jira + Linear editing (assignee, labels, sprint/cycle, estimate, due date, comments),
  a real-status Kanban board with drag-drop transitions, and issue→PR loops.

**Orchestration & resilience**
- Parallel fan-out: N agents race the same prompt in isolated worktrees; compare and merge the
  winner. Scoped sub-agents, an autonomous "heartbeat" that nudges a session to completion within a
  budget, worktree checkpoints (snapshot / roll back), and **Loops** (recurring autonomous ticket
  workflows).
- Cross-repo workspaces, git worktrees that share `node_modules` instead of reinstalling, SSH remotes
  (run and inspect a worktree on a remote box, with port-forward), and Design Mode (in-app browser
  element picker → the element's HTML/CSS into your prompt).
- Write-ahead transcript, a no-response watchdog that self-heals a dropped stream, one-tap session
  recovery, cost/token meters, quota probing, and multi-account credential hot-swap.

**Command Deck** — five destinations (Sessions · Loops · Fleet · Issues · Activity): a fleet
dashboard, an activity feed with a "needs you" inbox, and a Cmd-K palette.

## Get started

**1. Install the daemon on your Mac**

```sh
curl -fsSL https://howlerops.github.io/iron-rain/install.sh | sh
```

**2. Get the app** — macOS ships with the installer above; iOS is on **TestFlight**. Pair the phone
to your Mac once and you're driving sessions from either.

## Architecture

| Path | What it is |
| --- | --- |
| [`app/`](app/) | SwiftUI universal app (iOS + macOS). The product. |
| [`daemon/`](daemon/) | Go daemon that drives the agents and exposes the E2E-encrypted WebSocket protocol. |
| [`relay/`](relay/) | Stateless ciphertext forwarder for remote access (Cloudflare Durable Objects primary, Fly fallback; hosted or self-host). |
| [`protocol/`](protocol/) | The shared wire contract + parity test vectors. |

End-to-end encryption is enforced at the protocol layer — the relay only ever forwards ciphertext,
and approvals are first-class in the wire contract.

## Working on Iron Rain

Any agent (or human) can pick up work here: start with [`AGENTS.md`](AGENTS.md) and the portable
skills in [`skills/`](skills/). Every component ships with code + AGENTS docs + a skill.

## License

MIT — see [`LICENSE`](LICENSE).
