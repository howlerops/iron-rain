---
name: oculus-discovery
description: Autodetect active opencode/claude-code sessions on the host so they appear in the app with no manual config. Use when changing host discovery, the discover.list protocol message, or adding a new provider's autodetect path.
---

# Oculus session autodetection (`daemon/discovery`)

Discovers **active agent artifacts on the host** so they surface in the app automatically — the
"continue my terminal session on my phone" handoff. Best-effort: one failing scan never blocks others.

## What it finds
- **opencode servers** — `FindOpenCodeServers(ctx)` shells `lsof -nP -iTCP -sTCP:LISTEN`,
  `ParseListeners` extracts `(command,pid,port)`, keeps `command` prefixed `opencode`, dedupes by port
  → `http://127.0.0.1:<port>`.
- **opencode live sessions** — for each server, `listOpenCodeSessions` calls `opencode.New(url).List`
  (`GET /session`) → one `Discovered{kind:"session"}` each.
- **claude-code sessions** — `FindClaudeSessions(dir, within, now)` scans
  `~/.claude/projects/<encoded-cwd>/<id>.jsonl`, keeps transcripts modified within `within`
  (default 24h). `cwd` is a **best-effort** decode of the dir name (`"/"→"-"`, leading `-`); **lossy**
  for paths containing `-` (e.g. `opencode-mobile` decodes to `.../opencode/mobile`).

`Scan(ctx)` combines all three into `[]protocol.Discovered`.

## Protocol
`discover.list` (request, `TypeDiscover`, no payload) → `ok` with `DiscoverList{Items:[]Discovered}`.
`Discovered{provider,kind,url?,session_id?,title?,cwd?,path?,pid?}`, `kind ∈ {server,session}`.

## Wiring
`hub.SetDiscoverer(discovery.Scan)` (done in `main.go`). The hub answers `discover.list` with the
installed func, or an **empty list** if none is set (never an error). Injectable for tests.

## Testability
Everything OS-specific is a `var` you can swap: `lsof` (byte output) and `listOpenCodeSessions`.
`ParseListeners`, `FindClaudeSessions` (temp dir + injected `now`), and `combine` are pure.
`daemon/discovery/discovery_test.go` covers all of them; `daemon/hub/discover_test.go` drives
`discover.list` over the real encrypted transport with an injected scan.

## Live check
`oculusd discover` prints what it detects on this host. Verified live against real opencode 1.17.19
(`opencode serve` → session enumerated over the real HTTP API) and real claude-code transcripts.

## Adding a provider's autodetect
Add a `Find<Provider>…` returning host facts, fold it into `combine`/`Scan` as more `Discovered`
items. Keep the OS call behind a `var` so it stays unit-testable. See [[add-a-provider]].
