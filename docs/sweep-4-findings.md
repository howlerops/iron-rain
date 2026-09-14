# Fourth sweep — findings register

15 read-only finder agents audited the whole tree cold (daemon + Swift client) on
2026-09-14 and returned **71 findings**. Every finding below was reported with a concrete
trigger and observed failure; the ones marked FIXED were additionally verified by me
against the code before being acted on.

**Method note for whoever continues this.** Do not trust a finding because it is written
down here — several plausible ones were refuted on inspection, and two of the fixes below
needed their *tests* rewritten because the first version passed for the wrong reason. Read
the code, then write a test, then revert only the fix and confirm the test fails. A control
that still passes means the test is tautological.

That method earned its keep on the way through. Six more tests had to be rewritten after their
controls passed: one returned at an early guard without reaching the line under test, one compared
the wrong pair of source offsets, one asserted a growth property the old code also satisfied, one
staged a race the original shape could not lose, and two drove a helper instead of the branch that
contained the defect. Four of the findings themselves were wrong or overstated — see
**Corrections**. Every claim below is the version that survived being checked.

Finders were also asked to record what they checked and *refuted*. Those lists are in the
session transcript and are worth reading before re-auditing an area — they include things
like the relay's proven-beats-unproven pairing rule, the SSRF surface in `preview_fetch`,
the constant-time secret comparisons, and the nil-slice-as-`null` class, all of which were
examined and found clean.

---

## FIXED (71 of 71)

The register is closed. The first 22 shipped as **v0.2.199** (commits 67f1336, ac5979d, 6ee4277,
3281ed5, 623d6e5, dce864e, e5ea242, d7425a2); the remaining 49 are on main and **not tagged**
(586a970, 7313bfe, 31600ce, fd4da6c, b6fac07, 7e13a26, 343f25c, 8c2c3eb, 63948b8, 80f2131).

Every one has a test whose control fails when only that fix is reverted, with two stated exceptions
— the cli drain grace (fifth batch) and the subscribe window (sixth batch), each explained where it
sits. Four findings turned out to be wrong or overstated as written; those are recorded in
**Corrections** at the end rather than quietly fixed to match.

### First batch — the criticals

| Area | What | Where |
|---|---|---|
| fsaccess | A **dangling** symlink escaped the guard: `EvalSymlinks` errors when the target does not exist, so `resolveExisting` kept the literal in-root path and `os.WriteFile` followed the link out. Reproduced writing outside the root. | `fsaccess.go` `resolveExistingHops` |
| cli/hub | Every prompt on a **remote session** was executed by the remote login shell. `session.prompt` is capSteer, `remote.run` is capOwner. Added `{prompt_sh}`. | `hub.go:1994`, `cli.go substitute` |
| hub | Two unlocked reads of `meta.fanoutGroup`; a torn string header faults unrecoverably and kills the daemon. **The race test excluded `session.go` on a false premise** and is now switch-aware. | `session.go` `onStatus`/`info`, `meta_race_test.go` |
| claudecode | `Close()` deadlocked on `writeMu` before reaching `TerminateGroup`; `send` discarded every caller's context, wedging `Probe`. | `claudecode.go` |
| hub/mcp | opencode's MCP calls bypassed approvals via the **machine-wide token** (second route; the gateway-base route was fixed last sweep). Harness token + unattributed approval card. | `mcp.go`, `mcp_authz.go`, `hub.go` respond path |
| activity | Snapshot/write not atomic — events lost or duplicated. **Its own test was already failing** under `-count=30`; single-run suites hid it. | `activity.go` |

### Second batch — capability boundary

| Area | What | Where |
|---|---|---|
| roles | A `role.grant` demotion lived only on the socket; a credentialed device resolved back to `RoleOwner` on reconnect. Now persisted as `Device.Role` and checked first. | `devices.go`, `invites.go`, `roles.go` |
| devices | A revoked device kept every push (with session and approval ids). Tokens are now bound to the device and dropped in `closeDeviceConns`, which all four revocation paths pass through. | `hub.go`, `devices.go` |
| hub | `agent.list` served custom agents' `Env` — API keys — to watch-only guests. Roster stays capWatch, env is owner-only. | `hub.go` |
| hooks_guard | A shell redirect into `.git` walked past the protected-path rule; yolo mode installed hooks with no card. Shell command text is now scanned. | `hooks_guard.go` |
| fsaccess | `VCSMetadataComponent` answered "not metadata" for every relative path, which is the shape a shell token takes. | `fsaccess.go` |
| hub/mcp | Hosted (http) MCP servers were passed to harnesses with real URLs and stored API keys, skipping the gateway. All transports are now fronted. | `mcp.go` |
| mcp | The 10-minute approval window was unreachable — the authorizer got the 120s request context. | `gateway.go` |

### Third batch — data loss

| Area | What | Where |
|---|---|---|
| worktree | `RemoveWorkspace` deleted the uncommitted work it had just refused to delete. | `workspace.go` |
| worktree | `SweepOrphans` could delete live worktrees whose repo was merely unmounted. Now quarantines by time — a mount comes back, a deleted repo does not. | `worktree.go` |
| hub | The husk-drop orphaned the write-ahead JSONL, often the only copy of the user's prompts. | `persist.go` |

### Fourth batch — turn identity and lifecycle

| Area | What | Where |
|---|---|---|
| hub | `handleUnreachable` could abandon the turn that replaced the one it watched; the revive-success path forced `running` onto it. | `turn.go` |
| hub | The reconcile close was posted to the pump as a closure that re-checked nothing. | `turn.go` |
| hub | `approval.respond` deleted the approval before `Respond` was attempted, so a failed answer became a permanent ghost card. | `hub.go` |
| fanout | The judge session's pump was never started — no `go ms.run()` — so it produced nothing, ever. | `fanout_judge.go` |
| loops | `Upsert` dropped `LastRun`, so editing a loop reset its schedule and launched an unrequested autonomous run within a minute. | `loops.go` |
| issues | A DISCONNECTED tracker's tickets never left the board — the keep-cache loop keyed on success, which a disconnected provider can never achieve. | `manager.go` |

### Fifth batch — provider hangs that wedge a session permanently

Commits 586a970, 7313bfe, 31600ce. Not released; Phase 2 should land before the next tag.

Each of these had **no recovery path**: the session sat "working" forever and `Probe` reported
running, so the turn engine's reconciler never rescued it.

| Area | What | Where |
|---|---|---|
| pi | `send` took **no context at all** — `Prompt`, `Stop`, `Nudge`, `Respond` all named theirs `_` — so `promptBounded`'s 15s deadline was discarded. `heartbeatTick` walks sessions SERIALLY, so one wedged pi session stopped budget enforcement, handoff indexing and stall detection for every session behind it, with no log line. `heartbeat.go:406` documents this class of hang as fixed; it was fixed for claude-code only. | `pi.go` `sendCtx`, `thread.go` |
| claudecode, pi | `readLoop` returning early on a scanner error left the child parked in `write()` against a pipe nobody drains, so `cmd.Wait()` blocked forever and `closeEvents()` never ran. One frame over the cap (8 MB / 16 MB) — a large tool result — does it. `procutil`'s `WaitDelay` does not apply: its timer starts only once the ctx is cancelled or Wait has seen the exit. Now: drain into `io.Discard`, `TerminateGroup`, then reap. | `claudecode.go` / `pi.go` spawn goroutines |
| pi | `readLoop` never checked `sc.Err()`, so a truncated stream returned whatever `idle` held — a clean finish if the big line landed after an `agent_end`, the exit code blamed otherwise. Everything after it discarded silently. claudecode grew this check in 8d293ac. | `pi.go` `readLoop` |
| cli | `stream()` ran to stdout EOF **before** `cmd.Wait()`, so a backgrounded grandchild holding the inherited stdout kept the turn alive after the agent exited. Now reaps first and drains second, on an owned `os.Pipe` so the tail of the output is not cut by Wait. `orphan_test.go` could not have caught it — its fixture keeps the parent alive. | `cli.go` `runTurn` |

One honest caveat, recorded because this register is the place for it: the cli fix's
`streamDrainGrace` has **no negative control**. The "last line still arrived" assertion passes even
with the grace removed — the reader drains the 64 KiB a pipe holds faster than `waitpid` returns —
so that assertion is a guard against gross truncation, not proof the grace window works. The
assertion that has a control behind it is the one about the turn ending at all.

---

### Sixth batch — wrong account, wrong session, wrong ending

Commits fd4da6c, b6fac07, 7e13a26.

| Area | What | Where |
|---|---|---|
| hub | `SetAccounts` wired the account-env resolver onto a one-time SNAPSHOT of providers. `agent.upsert` and `provider.refresh` both re-`Register`, so Re-scan or saving a custom agent silently dropped it — sessions then spawned with the ambient environment while the Accounts screen still showed the account active. Wiring moved into `Register`, the one path every provider arrives by. | `hub.go` |
| opencode | `message.updated` was the only SSE case that neither decoded nor filtered `sessionID`, so every session sharing a directory claimed every other's cost, tokens and provider errors. | `opencode.go` |
| opencode | `Respond` deleted the approval→session mapping before the POST, so the todowrite retry sent attempts 2 and 3 to the parent. The sub-agent stayed blocked on a permission nobody was shown. | `opencode.go` |
| opencode | `Delete` ignored the HTTP status; every non-2xx read as success. | `opencode.go` |
| opencode | `sendParts` had no interlock, so a supervisor nudge's POST returning first declared the running turn idle — and reset `sawDelta` under it, duplicating the reply through `resyncLast`. | `opencode.go` |
| hub | `startSession` created a worktree and reserved a port, and only the BOOTSTRAP failure undid them. A provider that refuses to start leaked a checkout per retry, unreachable by every sweep because no session record was written. | `hub.go` |
| hub | The pump's panic recover returned normally, skipping the detach — the session stayed bound to a dead pump with unresolvable approvals and a live MCP token. Runs under its own recover now, and `mcpSessionTokens.revoke` is nil-safe: the first version of the fix crashed the test binary. | `session.go` |
| hub | `adoptForkedSession` copied the parent's whole `sessionMeta` while claiming project/cwd, so a fork inherited live ownership claims — resolving a fan-out tore down the kept winner's worktree. | `thread.go` |
| hub | The budget stop pushed twice, with two wordings of one fact. | `heartbeat.go` |
| hub | `subscribe` snapshotted the replay after registering, so a frame broadcast in between was delivered twice. Live frames are now held aside across the snapshot. | `session.go` |

### Seventh batch — daemon infrastructure

Commit 343f25c.

| Area | What | Where |
|---|---|---|
| lsp | On server replacement every open document was re-announced with `didOpen` BEFORE the `initialize` handshake, so the server dropped them and nothing re-sent. Previously-open tabs answered nothing forever while a newly opened file worked — which is what made LSP look recovered. | `lsp.go` |
| store | `PruneSessions` never evicted orphaned `handoffs` rows despite its comment. The table grew monotonically for the life of the install. | `store.go` |
| hub | `persistSessionAt` had no `ephemeral` guard, so the periodic touch wrote the record `addSession` had just refused to write — breaking the prune's own orphan invariant. | `persist.go` |
| hub | Persisting the user's prompt and echoing it were one function called only when the author was known, so an unidentified client's prompts were recorded nowhere: pi and CLI sessions restored as answers with no questions. | `session.go` |
| main | The startup banner printed the APNs bundle from the FLAG, not the value passed to `enablePush`. | `main.go` |

### Eighth batch — capability boundary, provider silences, loops

Commit 8c2c3eb.

| Area | What | Where |
|---|---|---|
| hub | `device.register` was ungated and excused by the census as per-connection. A push token is a standing subscription to hub-wide content, and the fan-out is not capability-aware. Now capSteer — a watch-only guest stops receiving push, deliberately. | `hub.go` |
| hub | `redeem()` consumed the invite seat before the enrolment decision, so a refused device permanently burned a one-use link. | `invites.go` |
| hub | The auth throttle slept per-attempt on the handshake goroutine, so penalties elapsed in parallel across sockets — the only rate control in the auth path provided none. Failures now queue. | `authlimit.go` |
| agui | An HTTP 200 that streamed nothing was reported as a finished turn: a misconfigured endpoint "finished" instantly with an empty reply and no error anywhere. Silence is now an error; a truncated run that delivered work still finishes. | `agui.go` |
| genui | The `iron:ui` fence body was bounded only after the whole body was resident, so an unterminated fence grew without limit and was forwarded to every client whole. | `genui.go` |
| loops | `MaxConcurrent` was a check-then-act with the lock released across the spawn, so two overlapping refreshes each started up to the cap — two agents on one worktree. | `loops.go` |
| loops | A task loop whose spawn failed set `Status:"error"` and dropped the reason; nothing logged either. | `loops.go` |
| fanout | The judge spec was deleted on the first summary broadcast, so the synthesis round — the one a judgement is most useful for — never got a judge. | `fanout_summary.go` |
| issues | The Jira `onRefresh` closure captured its adapter's cloud id, so a refresh landing after a site switch pointed the daemon back at the old site AND stranded the live adapter → `invalid_grant`, polling suspended. | `manager.go` |

### Ninth batch — the Swift client

Commits 63948b8, 80f2131.

| Area | What | Where |
|---|---|---|
| swift | A refusal rendered as an empty state on the three screens a guest actually lands on: Activity (the default iOS destination) said "No activity yet", Loops offered a New button that would also be refused, Issues invited them to connect the owner's tracker. `loadIssues` had to become a real request for a refusal to be observable at all. | `ActivityView`, `LoopsView`, `IssuesView`, `OculusUI` |
| swift | Seven actions reported success without checking: the loop editor's membership test (always true when editing), "Start agent" calling `onDone(true)` outside the Task, "Create ticket" reading a flag the call returns before clearing, two nil-into-Dictionary assignments that delete the key, MCP "Test", iOS add-by-path, and minting an invite. | eight files |
| swift | The fallback Kanban board had three columns for four categories, so canceled/duplicate/unmapped tickets vanished from the board while staying in List view. | `IssuesView` |
| swift | `nonRingFrameTypes` omitted three hub-wide broadcasts carrying `session_id`, inflating the transcript paging cursor — `session.heartbeat` without bound. Now backed by a census that reads the daemon's source. | `OculusUI` |
| swift | `WorktreePRResult.error` and `LoopRun.error` are sent by the daemon and had no client property, so a failed PR was indistinguishable from a success and a failed loop run had no reason. | `Protocol.swift` |
| swift | `delegateSubtask` had no `modelProvider`, so every delegation to opencode sent half an address and the child ran on the default model. | `OculusUI`, `ChatView` |
| swift | `removeWorktree` erased the on-device transcript before a fire-and-forget send (the twin of the `stopSession` bug), and `invokeUIAction` was a silent no-op after the card had latched to "Sent". | `OculusUI`, `GenerativeUI` |
| swift | "Delete session" — the only irreversible sidebar action — had no confirmation, and the Loops detail "Edit" button set the state it was already in. | `SessionSidebar`, `CommandDeck` |
| swift | Three per-frame costs made once-per-change: the sidebar grouping (3× per body pass at ~25 Hz), the diff's per-file counts, and the test pane's ungated scroll-to-bottom. | `SessionSidebar`, `DiffReviewView`, `ChatView` |

### Stale comments corrected

All four are done: `hub.go`'s false "(idempotent)", `MCPServersView` and `LoopsView` both claiming a
failed toggle could not be detected (the model had already been fixed to revert and raise a banner),
and `probe_test.go`'s `deafSidecar` claiming a sidecar "wedged so hard its stdin loop is gone" whose
script drains stdin on the next line. That last one is why the two wedged-sidecar defects went
uncaught: the fixture could not reach them.

---

## Corrections

Four findings were wrong or overstated as written. Recorded rather than quietly adjusted, because
the register's value depends on it being trustworthy about itself.

- **`session.go:925` subscribe — severity overstated.** The finding said the window is "wide for a
  restored session (full SQLite read + sha256 per frame)". It is not: `fullHistory` snapshots the
  ring under `m.mu` as its FIRST statement, and the durable read, the join and the hashing all happen
  after that snapshot. The real window is a few instructions. Measured at one duplicate in 300 rounds
  with six goroutines broadcasting into it, and zero in 300 with the fix. Real, worth fixing, and
  with no negative control achievable at a sane runtime — the deterministic test covers the
  mechanism instead, and says so.
- **`turn.go:888` nudge bound — half wrong.** "No `agent.Nudger` implementation reads the context"
  was true of claude-code and pi (both fixed in 586a970). opencode's `Nudge` names its context `_`
  and always will: `sendParts` hands the POST to a goroutine and returns, so it cannot block
  `turnLoops` however wedged the server is. Documented at the line.
- **`hub.go:4890` — wrong function.** The write-ahead transcript append is unconditional; the
  `author != ""` gate is on `broadcastUserEcho`, which is what persists the user's half of the
  RENDERABLE transcript. Same symptom, different line.
- **REMAINING count.** The header said 49 against a list of 50, with no duplicate to explain it.

One thing the sweep did not find, which its own method did: the ring-cursor census written for
`nonRingFrameTypes` immediately surfaced four more types the hand-maintained list had missed
(`approval.resolved`, `approval.rules.changed`, `fs.change`, `lsp.diagnostics`). All four were
checked against their payload structs and none carries a session id, so none was a live defect — but
none of them was in the register either.

---

## What is left

Nothing from this sweep. Two things worth carrying into the next one:

1. **The relay flake.** One run of the full suite hit `TestASlowClientLosesNoFrames` in `./relay`
   ("no host for server_id") under parallel load. It did not reproduce in 5 targeted runs, 12 runs
   of the whole relay package on the pre-change tree, or three further full-suite runs. `relay` is
   untouched by this work. It is a pre-existing intermittent, not a regression, and it is written
   down here so the next person does not rediscover it cold.
2. **The two exceptions above** — the cli drain grace and the subscribe window — are the only fixes
   in the register shipped without a control that fails. Both are argued at the line; neither is
   load-bearing enough to block, and both are the kind of thing a future change could silently undo.

The tag is deliberately not moved: `v0.2.199` shipped the first 22, and the other 49 want a real
exercise of the app before they are cut.
