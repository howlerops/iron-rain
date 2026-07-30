# Gap Roadmap — closing the ADE competitive gaps

**STATUS (2026-07-30, shipped across v0.2.107–v0.2.108):** this roadmap is substantially complete.

Delivered: all of §0 (0.1–0.9) · AP-1…AP-4 (scoped approval rules, payload enrichment, rules UI,
Ask/Architect/Code modes) · FO-1…FO-3 (fan-out aggregation, advisory judge, divided fan-out) ·
MCP-1…MCP-3 (registry, dual-stack client, per-harness injection) · **MCP-2 gateway** (one supervised
connection per server, bearer-authenticated local HTTP front door, protocol-revision bridging) ·
MU-1…MU-3 (principals, attribution, observer/steerer roles with owner-only approvals, presence) ·
§5 (10 builtin CLI agents) · §6 (screen capture) · G-1 (genui spec registry) · G-2 (the `form`
interpreter component, which also implemented the long-dead ui.action `Values` surface).

STILL OPEN, and why each was deferred rather than rushed:
- **MCP-5 per-server tool rules.** The gateway routes every tool call through the daemon, but an
  MCP request arrives on an HTTP connection with no session identity — gating it by the existing
  approval engine needs per-session gateway tokens first. Worth doing properly; not worth faking.
- **MCP-4** remote-server OAuth (PKCE, issuer-bound credentials, CIMD) and **MCP-5** registry
  browse/install. Both are external-integration work with real auth surface.
- **G-3** tool-event projection (todos→checklist, changed-files→diff) needs structured tool events
  per provider; **G-4** MCP Apps needs a sandboxed WKWebView bridge.
- **MU-4** invite flow (share-link → pairing lands as observer). MU-1…MU-3 shipped the model it
  would plug into.

The original plan follows unchanged, for the reasoning behind each decision.

Written 2026-07-30, after the Zed ADK / Orca ADE competitive analysis. This is the deep plan
for the seven ranked gaps, grounded in first-hand code mapping (file:line references verified
against the tree at v0.2.106) plus external research current as of this week — including the
MCP `2026-07-28` spec revision that shipped two days before this document.

Companion to `docs/turn-engine-plan.md` (shipped v0.2.106): same philosophy — daemon owns the
truth, clients render it; every stage independently shippable; live-wire smoke tests over mocks.

Ranked gaps this covers:

| # | Gap | Priority | Size |
|---|-----|----------|------|
| 1 | Daemon-owned MCP host + plugin surface | P0 | L |
| 2 | Argument/path-scoped approvals + Ask/Architect/Code modes + rules UI | P1 | M |
| 3 | Automated fan-out aggregation (differentiator — nobody has it) | P1 | L |
| 4 | Multi-user collaboration | P1 | XL |
| 5 | Broader agent registry | P2 | S |
| 6 | Visual element capture | P2 | S |
| 7 | Extensible genui catalog | P2 | M |

Plus §0: a batch of cross-cutting defects the research surfaced that should ship first.

---

## 0. Cross-cutting fixes (ship first — cheap, some are prerequisites)

Found while mapping the code for the gaps. Most are an afternoon each.

**0.1 — genui `plan` catalog drift (real model-facing bug).** `"plan"` is in
`knownComponents` (daemon/genui/genui.go:41-44) and rendered by Swift
(GenerativeUI.swift:35-45, aliased to checklist), but is absent from `skill.md` — the model
is never told the component exists. Fix: add to skill.md. Then add a **catalog sync test**:
adding a component touches 5 unenforced points (genui.go catalog, genui.go caps switch,
skill.md, the Swift switch, a props struct); only skill.md↔repo-mirror has a test today
(skillsync_test.go), which is exactly why `plan` drifted. New test: parse skill.md's
component list and assert equality with `knownComponents`.

**0.2 — protocol doc lie.** protocol.go:1282-1284 claims UI components come from a
"projection registry: todos→checklist, changed-files→diff". No projection code exists
anywhere — only the fence path is implemented. Fix the comment now; the projection idea
itself is revisited in §7.

**0.3 — `fanoutNotified` leak.** hub.go:88 map is never pruned. Prune on
`resolveFanout` and on group-member teardown.

**0.4 — process-group isolation (prerequisite for §1).** Zero `Setpgid` in the repo.
Every child (sidecar claudecode.go:263, pi.go:58, cli.go:204, opencode serve
autodetect.go:128, LSP server.go:48) dies via context-cancel or `Process.Kill()`, which does
not reap grandchildren. Anything launched as `npx -y foo` is a node wrapper with a grandchild
that leaks on session close. Fix: `SysProcAttr{Setpgid: true}` + negative-pid kill in one
shared helper (`daemon/procutil`), adopted by all five spawn sites. This alone fixes latent
leaks today and is mandatory before we supervise MCP servers.

**0.5 — child stderr never reaches the app log panel.** main.go:127 tees only the `log`
package into loghub; children writing to the inherited `os.Stderr` FD (claudecode.go:286,
pi.go:62) bypass it, and LSP stderr is discarded outright (server.go:79). Fix: pipe child
stderr through a scanner into loghub with a per-child prefix. MCP server diagnostics (§1)
would otherwise be invisible.

**0.6 — LSP dead servers stay dead.** When a server crashes, readLoop returns and the entry
stays in `m.servers` (lsp.go:83-86 returns the dead one) — every subsequent call fails
`errServerClosed` forever. Fix: evict-on-close + lazy respawn with exponential backoff.
Build this as shared supervisor code — §1's MCP manager needs the identical logic.

**0.7 — ui.action loose ends.** `kind=="permission"` is declared-but-deferred
(hub.go:3285-3286) and `Values` arrives on the wire but is never read. Either implement
(§7's form component needs `Values`) or delete the dead surface. Defer decision to §7;
document as known-dead in the meantime.

**0.8 — no signal handling anywhere: every graceful-shutdown defer is dead code.** Zero
`signal.Notify`/`os.Interrupt` hits in the daemon. `serve()` blocks in ListenAndServe and
the process is signal-killed, so `defer h.Shutdown()` (main.go:150), `defer db.Close()`
(:167) and `defer tr.Close()` (:179) never run — `lsp.Manager.Shutdown` (lsp.go:245-263)
is the only graceful child teardown in the codebase and it is unreachable in practice. On
every daemon exit the sidecars orphan (opencode is re-adopted next boot via the port file;
node/pi leak until their stdin dies). Self-update is worse: selfupdate.go:81 does an
in-process `syscall.Exec` with no child cleanup. Also: `cmd.WaitDelay` is used nowhere
(CommandContext = straight SIGKILL), and the managed opencode server never gets
`cmd.Wait()` at all (autodetect.go:142) — zombie. Fix: `signal.Notify(SIGTERM/SIGINT)` →
real shutdown path (children first, then stores), `WaitDelay` on all children, `Wait` the
opencode process. Pairs with 0.4 — together they are the process-hygiene prerequisite for §1.

**0.9 — agents.json read-modify-write race.** hub.go:2423-2438 (upsert) and :2456-2463
(delete) do `cli.Load → mutate → cli.Save` with no lock held across the RMW — and both run
on separate goroutines via the asyncDispatch allowlist (hub.go:1841-1923). Two concurrent
upserts can lose one. Every other settings file serializes through a mutex; agents.json is
the outlier. Also `cli.Save` (config.go:53-65) never MkdirAlls. Fix: serialize under a
registry mutex (loops.Engine pattern, see §1).

Suggested batch: 0.1–0.3 + 0.5 + 0.7-doc + 0.9 in one fix release; 0.4 + 0.6 + 0.8 as the
opening commits of the MCP host work (they are its foundation).

---

## 1. P0 — Daemon-owned MCP host + plugin surface

### Current state (verified)

No MCP code exists. Grep across daemon/app/docs finds only harness slash-command label
strings (commands.go:189/224/253) and loop-prompt prose. The `ironrain-ui` MCP from the
gen-UI plan is unbuilt. This is greenfield — but three near-exact templates exist in-tree:

- **`daemon/lsp/` is already a stdio JSON-RPC host.** MCP-over-stdio is JSON-RPC 2.0 with
  Content-Length framing — the same wire format as LSP. `rpc.go` readFrame/writeFrame is
  reusable verbatim. `server.go` has spawn (48-82), request/response correlation via
  `pending map[int64]chan` (108-124), ctx-aware `call` (161-189), once-only init handshake
  (209-220), graceful stop-then-kill (224-238). `lsp.Manager` (lsp.go:51-91) has lazy spawn
  keyed by root, with the lock never held across server I/O.
- **The agent registry is the plugin-surface template — for handler *shape* only.**
  protocol.go:41-46 + 1438-1477 (AgentInfo/AgentList/AgentUpsert/AgentRef/AgentVisible),
  hub dispatch at hub.go:2390-2493, visibility pair `agents.json`/`agent-visibility.json`.
  **Do not copy its persistence** — it has the §0.9 RMW race. The structural template for
  `mcp.*` is the **Loops engine**: loops.go:71-97 (Engine with its own mutex + `persisted`
  wrapper + `New(path)` loader), :373-383 `persist()` (snapshot under lock, write
  unlocked), handler arms hub.go:2556-2608 (list/upsert/delete/enabled), enable+broadcast
  wiring hub.go:737-751/:863-869 — loops is exactly a list/upsert/delete/toggle registry
  *with broadcast*, which agents.json is not.
- **PATH resolution is already solved** — `augmentPATH()` (main.go:589-647) runs a login
  shell and merges homebrew/npm/cargo/etc, which is what makes `npx`/`uvx`-launched servers
  findable under launchd.

### The spec landscape (research, 2026-07-30)

MCP revision **`2026-07-28`** landed two days ago and is the largest breaking change in the
protocol's history: stateless (no `initialize`/sessions), mandatory `server/discover`,
per-request `_meta` (protocolVersion/clientInfo/clientCapabilities), MRTR replacing
server-initiated elicitation/sampling, `subscriptions/listen`, cacheable list results
(`ttlMs`/`cacheScope`), `Mcp-Method`/`Mcp-Name` headers on streamable HTTP, CIMD replacing
DCR for OAuth. Roots/Sampling/Logging deprecated with a ≥12-month window.

Practical consequences for us:

1. **Dual-stack is mandatory.** ~18k registry servers speak `2025-11-25`-era protocol and
   won't migrate quickly; neither opencode nor Claude Code has confirmed new-spec support.
   Our client side negotiates per-server (probe `server/discover`, fall back to
   `initialize`). Our server side (the gateway, below) speaks what the harness clients
   speak — session-era `2025-11-25` — until they move.
2. **Don't build on deprecated surface.** No Roots (pass dirs as tool params), no Sampling
   dependency, stderr/OTel for logging.
3. **Extensions are opt-in by identifier** — `io.modelcontextprotocol/ui` (MCP Apps) and
   `/tasks` are the two worth planning for; MCP Apps ties directly into §7.

### Architecture decision: gateway, not config-sprawl

Two possible shapes:

- **(A) Config fanout** — daemon keeps a registry and injects per-harness config; each
  harness runs its own MCP client and spawns its own server processes. Cheap, but N
  harnesses × M servers of process sprawl, per-harness auth copies, secrets written into
  harness config files, no central audit.
- **(B) Daemon as gateway** — daemon supervises each server **once** and exposes it to
  harnesses over local streamable HTTP (`http://127.0.0.1:<port>/mcp/<name>`). Harness
  config becomes a trivial list of local HTTP URLs — a config shape *every* harness
  supports. Central lifecycle, single instance per server, secrets stay in `~/.oculus`,
  every tool call transits the daemon (→ ActivityStore audit, → future approval hooks,
  → MCP Apps interception for §7).

**Decision: (B).** The gateway is what makes this a *daemon-owned* host rather than a
config manager, and it is the moat versus Zed (whose context servers are per-editor) —
one server instance shared by every agent on the machine, visible and controllable from
the phone.

Harness injection points (all verified):

| Harness | Mechanism | Where |
|---|---|---|
| claude-code | `options.mcpServers` in the sidecar (Agent SDK ≥0.3.212, sdk.d.ts:1669) + **live hot-swap** `query.setMcpServers()` (sdk.d.ts:2507) — mirror the existing model-switch frame (claudecode.go:420 → sidecar.mjs:134-139). Env `OCULUS_MCP_CONFIG` at claudecode.go:267, parsed before sidecar.mjs:144. Sidecar is embedded + auto-refreshed on daemon upgrade (autodetect.go:281-306) so a JS-only change ships for free. | new `{t:"mcp"}` frame |
| cli (BYO: codex, gemini, claude-as-CLI…) | new `{mcp_config}` substitution token at cli.go:321-337 expanding to a daemon-written config path; user wires `--mcp-config {mcp_config}` in Args. Two-line change + file writer. | cli.go:321 |
| opencode | `OPENCODE_CONFIG_CONTENT` (inline JSON env — merges, doesn't replace) with an `mcp` block of `{type:"remote", url:"http://127.0.0.1:…"}` entries, set on the `opencode serve` process at autodetect.go:134. **MUST be gated on "we started this server"** — autodetect.go:92-116 has three paths (reuse-ours / start-ours / fall back to the user's own running opencode) and mutating config in the fallback path would leak our servers into the user's personal opencode. | autodetect.go:128-141 |
| pi | `cmd.Env` is never set (pi.go:58-62, inherits wholesale); adapter is a flagged spike. Defer until the pi MCP surface is verified. | pi.go:58 |

### Registry + protocol + UI

- `~/.oculus/mcp.json` + `~/.oculus/mcp-disabled.json`, modeled on the
  agents.json/agent-visibility.json pair. Entry:
  `{name, transport: "stdio"|"http", command, args, env, url, headers, enabled, scope}`.
- Protocol: `mcp.list` / `mcp.upsert` / `mcp.delete` / `mcp.enable` / `mcp.status`
  (+ `mcp.tools` for the inspector). New-command recipe (all seven steps, in order):
  protocol const + payload structs (protocol.go) → dispatch arm (hub.go:1997+ switch;
  unmarshal → validate → sendErr-or-mutate → broadcast → sendOK) → **add to
  `asyncDispatch` (hub.go:1841-1923)** or a slow server spawn blocks that client's entire
  read loop → Hub fields + `Set…Path` setter wired from main.go:188-194 → Swift mirror in
  Protocol.swift (use AgentInfo's decodeIfPresent-with-defaults init, :879-911, so an
  older daemon degrades gracefully) → Model async method + broadcast-switch arm in
  OculusUI.swift. Note the protocol/ vectors dir only locks four messages — adding a
  vector is not enforced; add one for `mcp.upsert` anyway.
- **Cross-device sync:** `mcp.enable` must broadcast (loop/provider pattern, hub.go:1535),
  not reply-to-caller-only — precedents to avoid: agent.list is never broadcast (second
  device's ManageAgentsView goes stale until reopened), notify.prefs.set replies to caller
  only (hub.go:2115), and the default agent is client-local UserDefaults that never syncs
  (OculusUI.swift:891-906). A phone toggling a server while the Mac watches is the demo —
  make it live.
- App: `ManageMCPView` modeled on AgentsView.swift (list + editor + per-server status dot
  like Zed's green dot), reached from the sidebar overflow menu. Tool inspector per server
  (name/description list from `tools/list`) for verification.

### Net-new hard parts (nothing in-tree does these)

1. Process groups + grandchild reaping (§0.4) **and** real shutdown: signal handling +
   `WaitDelay` (§0.8). Today the only crash-restart supervision that exists at all is
   launchd's `KeepAlive true` on the daemon itself (LoginItemManager.swift:64-102 /
   install.sh:81-99) — if MCP servers are daemon children, launchd restarting the daemon
   is currently their entire restart story.
2. Restart with exponential backoff + health checking (§0.6; LSP never restarts, opencode
   is start-once). In-repo templates to reuse: opencode `reconnectEvents`
   (opencode.go:555-586 — 500ms→15s cap, warn-once-then-keep-retrying, recovered notice),
   relayHost full-jitter backoff with "ran >5s ⇒ reset" (main.go:367-384),
   crash-vs-intentional classifier via non-blocking done-select (pi.go:88-110), give-up
   budget constants (heartbeat.go:21-30). Note the Turn Engine's own carve-out
   (turn.go:237-240): subprocess agents are supervised by stream-EOF, not probes — an MCP
   server has no turn stream, so it needs a real probe (periodic `tools/list` or ping
   against old-spec; `server/discover` on new-spec). Half-open detection:
   `idleTimeoutConn` (opencode.go:41-92) for HTTP transports. Port allocation for the
   gateway fleet: `worktree.AllocPort` (setup.go:143-152), not the race-prone
   `freePort()`.
3. stderr → loghub (§0.5) — **as a file or drained pipe, never an unread pipe.** Two
   landmines already documented in-tree: an app-held pipe dies with the app and the
   child's next write takes SIGPIPE (DaemonLauncher.swift:62-71 — this can *kill* the
   child), and an attached-but-unread pipe fills and blocks the child (why lsp
   server.go:79 discards). The daemon must own the read loop for as long as the child lives.
4. Secrets: server env/headers land in `mcp.json` 0600 like agents.json for v1; OAuth
   tokens (stage 4) issuer-bound in a separate 0600 store per SEP-2352 — never into
   harness-visible config (the gateway makes this possible: harnesses only ever see the
   local URL).
5. Per-request `_meta` stamping, MRTR loop, cache honoring (`ttlMs`) for new-spec servers.

### Stages (each shippable)

- **MCP-1 — Supervisor + registry + UI.** `daemon/mcp` manager forked from `daemon/lsp`
  (rpc.go reused; + procgroups, backoff, health, stderr capture), `mcp.json` CRUD protocol,
  ManageMCPView with status + tool inspector. Servers run and are inspectable; no harness
  sees them yet. Smoke: `daemon/cmd/mcp-smoke` (turn-smoke pattern) spawning the reference
  `everything` server, asserting tools/list round-trip, kill-and-observe-restart, and
  grandchild-reap (spawn via npx, kill group, assert no orphan).
- **MCP-2 — Gateway.** Streamable-HTTP endpoint on the daemon (session-era spec toward
  harnesses; dual-stack negotiation toward servers), one URL per server. Auth: bearer from
  the existing daemon key (main.go:551) so a rogue local process can't ride the gateway.
  Every `tools/call` recorded to ActivityStore.
- **MCP-3 — Harness injection.** claude-code (`OCULUS_MCP_CONFIG` + hot-swap frame → new
  servers usable mid-session), cli `{mcp_config}` token, opencode env-content gated on
  daemon-started. Per-session toggle UI ("which servers does this session see") using the
  hot-swap path on claude-code; session-start config elsewhere.
- **MCP-4 — Remote servers + OAuth.** `type:"http"` upstreams proxied through the gateway;
  OAuth 2.1 client (PKCE, `iss` validation, issuer-keyed creds, `application_type`, CIMD
  preferred) with the auth dance surfaced in the app (open browser / device flow). This is
  a feature Zed does per-editor and mobile can't do at all today — pairing-style UX win.
- **MCP-5 — Ecosystem.** Registry browse/search (registry.modelcontextprotocol.io API,
  frozen v0.1, `/v0/servers`) → one-tap install; per-project scoping (`scope` field +
  project registry); per-server tool allow/deny wired into §2's rule engine; MCP Apps
  (`io.modelcontextprotocol/ui`) interception at the gateway feeding §7; ship `ironrain-ui`
  as an MCP server for interactive gen-UI per the gen-UI plan.

---

## 2. P1 — Scoped approvals, modes, rules UI

### Current state (verified)

The wire already carries more than we use, and each hop drops data:

- `protocol.ApprovalRequest` (protocol.go:1184) **already has `Input json.RawMessage`** —
  only opencode populates it; the Swift model (Protocol.swift:453-464) doesn't decode it.
- The daemon intercept (session.go:612-624) calls `recordApproval(ar.ApprovalID, ar.Tool, m)`
  (hub.go:1388-1396) — **Detail and Input are discarded**, so the respond handler
  (hub.go:3311-3340) can only persist `provider|tool` when the user hits Always
  (approval_rules.go:18, on-disk format is a bare `[]string`).
- **opencode** (opencode.go:795-880) delivers `Patterns []string` — a ready-made glob/scope
  channel we throw away except `Patterns[0]` as display text — plus `Metadata` (the raw
  input). It natively supports server-side `"always"` (opencode.go:1056-1081).
- **claude-code** sidecar (sidecar.mjs:78-88) holds the full `input` object in its closure
  and forwards only a truncated 160-char `detail` string. Its response path supports
  `updatedInput` — a rules engine could even *rewrite* arguments. `always` collapses to
  `allow` (claudecode.go:408-414); persistence is ours alone.
- **pi** has `Args map[string]any` on the event (pi.go:187-200) and forwards none of it;
  **cli** has no approvals at all (cli.go:291-293).
- Plan mode is create-time-only (`SessionCreate.Plan`, hub.go:306-314 → `agent.PlanCreator`).
  But: opencode sends its agent/mode **per message** (opencode.go:969 `body["agent"]`) so
  it's already live-switchable; claude-code's sidecar sets `permissionMode` once
  (sidecar.mjs:143-148) with `acceptEdits`/`bypassPermissions` unwired.

### Design

**Rules engine** (`daemon/hub/approval_rules.go` grows up):

```go
type ApprovalRule struct {
    Provider   string `json:"provider"`             // "" = any
    Tool       string `json:"tool"`                 // "" = any
    Pattern    string `json:"pattern,omitempty"`    // glob vs command/args/URL detail
    PathPrefix string `json:"path_prefix,omitempty"`// normalized, fsaccess-style
    ProjectID  string `json:"project_id,omitempty"` // "" = global
    Action     string `json:"action"`               // allow | deny
}
```

Ordered, first-match; deny rules win ties (evaluated first). Migration: existing
`[]string` `"provider|tool"` entries load as `{Provider, Tool, Action:"allow"}` — invisible
upgrade, existing tests (approval_rules_test.go) keep passing plus new table tests. Path
matching reuses `daemon/fsaccess` normalization (allowed-roots + symlink-escape guards) —
do not write new path-matching code. Precedence mirrors Zed's proven order:
built-in-guard → deny → confirm(default) → allow.

**Stop dropping data:** `recordApproval` stores the whole `ApprovalRequest` + `m.meta`
(the scope vocabulary — projectID/cwd/repoRoot — is already on `sessionMeta`,
session.go:176-203). Sidecar sends `input` (one line at sidecar.mjs:86); claudecode.go:504
forwards it. pi forwards `Args`. Daemon adds `SuggestedPatterns []string` to
`ApprovalRequest`: opencode's `Patterns` verbatim; for bash a first-token(s) prefix glob
(`git *`); for file tools a directory prefix. Client decodes `input` + suggestions.

**Scoped Always:** `ApprovalRespond` gains
`Scope {Kind: "tool"|"pattern"|"project", Pattern string}`. ApprovalCard's Always becomes a
menu: "Always for `git *`" / "Always for this tool" / "Always in this project" — options
built from the daemon's suggestions, so the client stays dumb.

**Modes as rule-set presets, enforced daemon-side.** The insight: because every approval
transits the hub, modes work for **all** providers by policy, with provider-native hints
where available:

- `Ask` — read-only: hub auto-denies mutating tools (edit/write/bash-mutation), everything
  else auto-asks. Provider hint: opencode `body["agent"]="plan"`; claude-code
  `permissionMode:"plan"`.
- `Architect` — plan-first: native plan modes; mutations denied until mode switch.
- `Code` — normal: rules engine as configured.

Protocol: `session.mode.set {session_id, mode}` + `Mode string` on `SessionCreate`
(`Plan bool` kept as deprecated alias). New `agent.ModeSetter` interface next to
`PlanCreator` (agent.go:101-106); opencode implements it trivially (per-message field);
claude-code via a sidecar `{t:"mode"}` frame if the SDK exposes runtime permission-mode
switching, else applies at next turn + hub enforcement covers the gap immediately.
This directly beats Zed's open wound: their `tool_permissions` don't apply to external
ACP agents at all (zed#57355 et al.) — ours apply to every harness uniformly.

**Rules UI:** `ManageApprovalRulesView` — CRUD list modeled on AgentsView.swift, protocol
round-trip modeled on notify-prefs (notify_prefs.go end-to-end, hub.go:2106-2116,
OculusUI.swift:1618-1636). Reached from the sidebar overflow menu; shows rule source
("created from approval on <date>"), per-project grouping, delete/edit.

### Stages

- **AP-1** — structured rule store + migration + deny + table tests (daemon-only, invisible).
- **AP-2** — payload enrichment (sidecar input, pi args, suggestions) + scoped-Always menu
  in ApprovalCard + client `input` display (show the actual diff/command being approved).
- **AP-3** — ManageApprovalRulesView + project scoping.
- **AP-4** — modes: protocol + hub enforcement + provider hints + composer mode picker.
  (Sequenced after AP-1 because enforcement *is* the rule engine.)

---

## 3. P1 — Automated fan-out aggregation (the differentiator)

### Current state (verified) — half of this already ships

`fanout.create` / `fanout.resolve` are wired end-to-end (protocol.go:454-489, hub.go
spawnFanout :1089-1139 / resolveFanout :1145-1175, FanoutSheet.swift, 3 e2e tests in
fanout_e2e_test.go). Variants get worktrees (`Worktree:true`), per-variant models cycle,
sessions carry `FanoutGroup`/`FanoutVariant` tags. **`checkFanoutDone` (hub.go:1179-1212)
is the aggregation hook** — fired on any grouped session going idle, fires a push when all
members settle. Orca fans out too (worktree-per-agent, 30+ CLIs) — but *nobody* aggregates;
every product makes the human diff N variants by hand. That's the gap.

Result capture is recoverable without new agent work:

- **`store.Handoffs(cwd)` (store.go:275) returns agent-authored `{Title, Summary}` per
  session** — pre-digested results, no transcript replay.
- Fallback: `store.Transcript(sid)` tail (finalizeTurnTranscript persists the final
  assistant message, session.go:543-555).
- Worktree + `baseCommit` on sessionMeta → `git diff --stat` per variant.
- The Loops engine (loops.go) is the orchestration template: injected `spawn`, injected
  broadcast, concurrency cap, dedup, whole-state persist, `SetRunStatus` completion
  callback. `spawnChild`/`buildChildPrompt` (hub.go:1282-1333) is the programmatic
  worker-session recipe.

Known holes to fix on the way (some in §0): no durable group record (`fanoutGroup` lives
only on in-memory `sessionMeta`), no per-variant results, `fanoutNotified` leak (§0.3),
activity feed borrows `KindLoopRun` (hub.go:1135) instead of a fanout kind.

### Design

New `daemon/fanout` package (loops-style: engine + JSON persist at `~/.oculus/fanouts.json`):

```go
type Group struct {
    ID, Prompt, ProjectID string
    CreatedAt             time.Time
    Members               []Member  // SessionID, Variant, Model, Status,
                                    // Handoff {Title,Summary}, DiffStat, DurationSec
    Verdict               *Verdict  // optional judge output (stage FO-2)
}
```

**FO-1 — Compare view.** On `checkFanoutDone`: collect per-variant handoff (fallback:
transcript tail), diffstat vs `baseCommit`, duration; persist the Group; broadcast
`fanout.summary`; new `ActivityEvent` kind `fanout_done` (+ optional group field on
ActivityEvent — today `Project` is the only grouping key, activity.go). App: FanoutSheet
grows a results mode — side-by-side comparison rendered with **our own genui table/diff
components** (dogfood §7), tap into any variant's session, Keep wires to the existing
`fanout.resolve`. This alone is demo gold: "fan 4 models at one task from your phone,
compare summaries + diffstats, tap Keep."

**FO-2 — Judge.** Optional auto-synthesis: on group completion, daemon spawns a judge
session (spawnChild recipe; judge model configurable, defaults to the fastest available)
whose prompt embeds each variant's handoff + diffstat + (bounded) diff. Judge answers
through a genui `choice` component — recommendation with rationale, where tapping the
choice **is** `fanout.resolve` (the ui.action → prompt path already exists; add a
`fanout_resolve` action kind or route via the existing prompt round-trip). Guard rails:
judge is advisory, Keep always available manually; judge session is tagged so it never
counts as a fanout member.

**FO-3 — Task fan-out (decompose mode).** `FanoutCreate` grows `Prompts []string` (per
worker) or a `Decompose bool` where a planner session splits one task into N subtasks
first. Aggregation = sequential merge of worktrees or PR-per-subtask (reuse loop PR
machinery). This is Workflow-lite and considerably bigger — gated on FO-1/2 proving the
UX. Keep the protocol shape forward-compatible now (add `Prompts` field, clamp semantics
documented) so FO-3 needs no breaking change.

Tests: extend fanout_e2e_test.go (group persists, summary fires once, leak pruned);
judge e2e with a scripted cli provider; smoke via turn-smoke `-mode fanout`.

---

## 4. P1-XL — Multi-user collaboration

### Research grounding

- **Zed** (most mature): Guest/Member/Admin roles, guests read-only with an explicit,
  revocable, call-scoped write grant; permissions inherit down channels. Pattern to copy.
- **GitHub Next "Ace"**: multiple humans prompt the same agent; the chat log *is* the
  prompt context — no turn-taking arbitration. Elegant, and nearly free for us: the daemon
  already owns an ordered, gap-free event stream (Turn Engine seq/cursor).
- **Cursor's failure mode** (live security complaint): session runs with the initiator's
  OAuth identity while the prompt surface isn't bound to that user — teammates can steer a
  session that *acts as someone else*, invisibly. The lesson: **separate "who can
  see/steer" from "whose credentials the agent acts with," and display the acting identity
  persistently.**

Nobody has published a credible full permission model; a small correct v1 beats a big
vague one.

### Design (v1 scope, deliberately minimal)

1. **Principals.** Pairing already mints device keys; add
   `Principal {ID, Name, Role}` per paired device (a person may own several devices →
   principals reference a person record in `~/.oculus/people.json`). Every ws/relay
   connection resolves to a principal. The relay protocol (`?sid=&role=`) extends `role`
   from transport-role to principal auth.
2. **Two roles + owner.** `observer` (read-only: transcript, diffs, tool calls, meters)
   and `steerer` (prompt, interrupt, answer *non-destructive* approvals). The session
   **owner** is whose CLI credentials/git identity the agent acts with — shown persistently
   in the session header. Owner grants/revokes steer; new joiners default observer.
   Approvals: owner-only in v1 (simplest correct rule).
3. **Attribution.** `SessionMessage` gains `Author` (principal ID + display name); every
   prompt/interrupt/approval is attributed in the durable transcript (audit falls out of
   the SQLite store for free) and chipped in the UI.
4. **Concurrency = none needed.** All human messages append to the one ordered log; the
   agent reads the log (Ace's model). Daemon ordering is already authoritative.
5. **Presence.** Who's connected / typing / last-prompted via `broadcastTransient` (the
   turn-heartbeat pattern — never recorded, never replayed).

Out of scope for v1 (explicitly): multiplayer co-editing (OT/CRDT), voice, per-file ACLs.
No shipping competitor has these bound to agent sessions either.

### Stages

- **MU-1** — principals + attribution: people.json, principal on every connection,
  `Author` through prompt→transcript→UI. Ships value alone (multi-device users see which
  device sent what).
- **MU-2** — roles + enforcement: daemon gates prompt/interrupt/approval by role;
  grant/revoke UI in session header; owner badge.
- **MU-3** — presence + typing via transient broadcasts.
- **MU-4** — invite flow: share-link via relay → pairing lands as observer; revocation;
  per-session (not per-daemon) visibility grants.

Dependency note: MU rides the existing relay/pairing and Turn Engine; no protocol rework
needed. It is XL because of surface area (every input path gains identity), not depth.

---

## 5. P2 — Broader agent registry

Small, mostly catalog work. `cli.Builtins()` (config.go:20-25) has codex / gemini /
cursor-agent / aider; Orca ships 30+. Add builtin templates (command, args with `{prompt}`,
resume args, model lists, PATH detection) for: copilot CLI, goose, grok CLI, kimi, amp,
devin CLI — whatever `Detect()` can probe. Each is a struct literal + a detection probe;
`agentList()` (hub.go:1466-1532) already classifies and merges. Also: per-agent default
mode/rules hooks once §2 lands. One release, opportunistic.

---

## 6. P2 — Visual element capture

Attachments already flow (doc/image attach with text extraction, v0.2.17). v1 macOS:
ScreenCaptureKit window/region picker from the composer → attach as image (+ optional
quick-annotate: arrow/box overlay before send). v1 iOS: photo-library/screenshot attach
already works; add share-extension intake later. v2 tie-in: sessions carry a `port` on
sessionMeta — "capture the running app" button screenshots `localhost:<port>` via WKWebView
snapshot for web projects. Small, self-contained, demo-friendly.

---

## 7. P2 — Extensible genui catalog

### Current state (verified)

Daemon side is *already generic*: `Props json.RawMessage` opaque, per-component `SchemaV`,
mandatory `fallbackText` synthesized (genui.go:192) — adding names daemon-side ahead of
client support is non-breaking by construction. The two catalog touchpoints are
`knownComponents` (genui.go:41-44) and the `propsWithinCaps` switch (genui.go:209 — cases
only for table and choice/confirm). The hard wall is the compiled Swift switch
(GenerativeUI.swift:35-45).

### Design

- **G-1 — registry refactor + sync test.** Replace `knownComponents` + caps switch with
  `map[string]Spec{Name, SchemaV, Validate}`; §0.1's sync test enforces
  catalog↔skill.md↔Swift parity (Swift side: a generated list test in OculusKit tests).
- **G-2 — one interpreter component instead of N cases.** Add `"form"` (fields:
  text/select/toggle/slider → submits via ui.action `Values` — finally implementing §0.7)
  and `"layout"` (declarative rows/cols/metric-tiles composing existing components). Two
  new Swift cases, unlimited downstream shapes; the closed-catalog safety model is
  preserved because the interpreter validates against a typed schema.
- **G-3 — projection registry (make §0.2's doc true).** Daemon-side projections from
  structured tool events → components (todos→checklist, changed-files→diff, test
  results→table) for harnesses that emit structured events; keeps working where models
  never learned iron:ui.
- **G-4 — MCP Apps bridge (after MCP-2).** Gateway intercepts
  `io.modelcontextprotocol/ui` templates; render in a sandboxed WKWebView with the
  postMessage↔JSON-RPC bridge routed through the daemon so UI-initiated actions hit the
  same approval/audit path as tool calls. This is the interactive-genui endgame and
  subsumes the `ironrain-ui` MCP plan.

---

## Sequencing — proposed release train

Dependencies: §0.4/0.6 → MCP-1; AP-1 → AP-4 (modes) and MCP-5 (per-server tool rules);
MCP-2 → G-4; handoffs (shipped) → FO-1; relay+pairing (shipped) → MU-1.

| Release | Contents | Why this order |
|---|---|---|
| v0.2.107 | §0 fix batch (0.1, 0.2, 0.3, 0.5, 0.7-doc, 0.9) + AP-1 (invisible rules upgrade) + §5 registry breadth | Cheap wins, zero-risk, clears debt |
| v0.2.108 | AP-2 + AP-3 (scoped Always + input display + rules UI) | First user-visible approvals win; small |
| v0.2.109 | FO-1 compare view | Differentiator, demo-able, mostly-built plumbing |
| v0.2.110 | FO-2 judge + §6 visual capture v1 | Completes the fan-out story |
| v0.2.111–112 | §0.4 + §0.6 + §0.8 then MCP-1 + MCP-2 (supervisor, registry, UI, gateway) | The P0, sequenced after quick wins because it's the long pole |
| v0.2.113 | MCP-3 injection + AP-4 modes | Modes reuse the rule engine; MCP servers reach every harness |
| v0.2.114+ | MCP-4 OAuth/remote, MCP-5 ecosystem, G-1..3 | Ecosystem depth |
| parallel track | MU-1 → MU-4 across releases | Independent of the MCP/approvals track |
| later | FO-3 decompose, G-4 MCP Apps | Gated on earlier stages proving out |

### Testing strategy (per turn-engine precedent)

- Unit: rules-engine table tests (match order, deny-wins, migration), genui registry/sync,
  fanout collector, principal resolution.
- Hub e2e: extend fanout_e2e_test.go; approvals e2e with scripted providers; mode
  enforcement e2e (Ask-mode denies bash).
- Live-wire smokes (`daemon/cmd/*-smoke`, turn-smoke pattern): `mcp-smoke` (spawn
  reference server → tools/list through gateway → kill → observe restart → grandchild
  reap), fanout smoke, multi-principal smoke (two ws connections, role gating).
- App: OculusKit `swift build` + tests; manual checklists per release as with v0.2.106.

### What we deliberately do NOT build

- A marketplace/sandboxed-plugin runtime (Zed's WASM extensions) — MCP *is* our plugin
  surface; the registry browse (MCP-5) is our marketplace.
- ACP adapter — worth watching (opencode + Zed both speak it), but our per-provider
  adapters + Turn Engine already exceed ACP's session/permission model; revisit if a
  major harness ships ACP-only.
- CRDT co-editing, voice presence — out of MU scope until someone actually asks.
