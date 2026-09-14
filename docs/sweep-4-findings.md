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

Finders were also asked to record what they checked and *refuted*. Those lists are in the
session transcript and are worth reading before re-auditing an area — they include things
like the relay's proven-beats-unproven pairing rule, the SSRF surface in `preview_fetch`,
the constant-time secret comparisons, and the nil-slice-as-`null` class, all of which were
examined and found clean.

---

## FIXED (6) — commits 67f1336, ac5979d, 6ee4277

| Area | What | Where |
|---|---|---|
| fsaccess | A **dangling** symlink escaped the guard: `EvalSymlinks` errors when the target does not exist, so `resolveExisting` kept the literal in-root path and `os.WriteFile` followed the link out. Reproduced writing outside the root. | `fsaccess.go` `resolveExistingHops` |
| cli/hub | Every prompt on a **remote session** was executed by the remote login shell. `session.prompt` is capSteer, `remote.run` is capOwner. Added `{prompt_sh}`. | `hub.go:1994`, `cli.go substitute` |
| hub | Two unlocked reads of `meta.fanoutGroup`; a torn string header faults unrecoverably and kills the daemon. **The race test excluded `session.go` on a false premise** and is now switch-aware. | `session.go` `onStatus`/`info`, `meta_race_test.go` |
| claudecode | `Close()` deadlocked on `writeMu` before reaching `TerminateGroup`; `send` discarded every caller's context, wedging `Probe`. | `claudecode.go` |
| hub/mcp | opencode's MCP calls bypassed approvals via the **machine-wide token** (second route; the gateway-base route was fixed last sweep). Harness token + unattributed approval card. | `mcp.go`, `mcp_authz.go`, `hub.go` respond path |
| activity | Snapshot/write not atomic — events lost or duplicated. **Its own test was already failing** under `-count=30`; single-run suites hid it. | `activity.go` |

---

## REMAINING (65)

### Security / capability boundary

- **HIGH** `hub/invites.go:355`, `roles.go:98`, `hub.go:2660` — a `role.grant` demotion is stored only against the live `*transport.Conn` and never persisted, so a demoted **credentialed** device resolves back to `RoleOwner` on reconnect. No `role` field on `Device`; `roleRegistry` has no disk backing. Re-demoting lasts until the next disconnect.
- **HIGH** `hub/hub.go:5302` — `device.register` has **no capability gate**, and an APNs token is never removed on device or invite revocation. A revoked observer keeps receiving every push (with `session_id` and `approval_id`) indefinitely; the list is pruned only when APNs reports the token dead.
- **HIGH** `hub/hub.go:3611` — `agent.list` is capWatch and its reply carries each custom agent's `Env`, which the app's own UI says can hold API keys. `account.list` is capOwner for exactly this reason. Removing `TypeAgentList` from `watcherReads` is required either way or the census's stale-entry check fires.
- **HIGH** `hub/hooks_guard.go:140` — the `.git`/protected-path guard never inspects a shell tool's `command` argument, so a redirect (`… > .git/hooks/pre-commit`) defeats it. `looksLikePath` rejects anything with a space; `command` is not in `pathKeys`. The daemon's own `git commit` (no `--no-verify`) then runs it.
- **HIGH** `hub/mcp.go:387` — an **http-transport** MCP server is injected into the harness with its real URL and stored `Headers` (API keys) instead of being rewritten to the gateway, so its calls never reach `authorizeMCPTool`. `Manager.Dial` already proxies hosted servers, so this is not a capability limit.
- **MEDIUM** `hub/invites.go:117` — `redeem()` consumes the device slot **before** the enrollment decision, so a refused device (revoked) permanently burns a seat. Owner sees "Redeemed 1/1" for a link nobody used.
- **MEDIUM** `hub/authlimit.go:35` — the auth throttle is a per-attempt `time.Sleep` on the handshake goroutine, so penalties elapse in parallel across N concurrent sockets. The documented "~1 guess/sec" bound does not hold. Not a practical break against 128-bit credentials; the defect is that the only rate control in the auth path provides none.
- **MEDIUM** `mcp/gateway.go:174` + `hub/mcp_authz.go:255` — the 10-minute MCP approval window is unreachable: the authorizer receives the gateway's 120s request context, so every MCP approval is force-denied at two minutes with the wrong reason ("cancelled before anyone answered").

### Data loss

- **HIGH** `worktree/workspace.go:109` — `RemoveWorkspace` runs `os.RemoveAll(layout)` **unconditionally**, even when every member's `git worktree remove` refused for uncommitted work. The user is shown the refusal while the work is already gone. `workspace_test.go` only exercises `force=true`.
- **MEDIUM** `worktree/worktree.go:320` — `isOrphanWorktree` decides purely on whether the gitdir path stats, so an unmounted volume or moved repo makes `SweepOrphans` (runs at **every daemon start**) delete live worktrees and their uncommitted work. No `IsDirty`, no `rev-list`, no force gate.
- **MEDIUM** `hub/persist.go:201` — the husk-drop deletes the session record and SQLite transcript but not the write-ahead JSONL, and deleting the record makes that file unreachable forever (the sweep enumerates ids from `ExpiringSessions`).

### Turn engine / lifecycle

- **HIGH** `hub/turn.go:766` — `handleUnreachable` checks only that *a* turn is open, never that it is the one it supervises, so after the ≤20s `Revive` it can abandon the turn opened after it. The success path at `:746` has the same defect, forcing `running` onto a turn it knows nothing about.
- **HIGH** `hub/turn.go:1164` — the reconcile close is posted to the pump as a closure that re-checks nothing; every sibling path calls `stillMine(turnID)`. A freshly-opened turn ends instantly as "reconciled: completion event was lost".
- **HIGH** `hub/session.go:925` — `subscribe` registers the subscriber then **releases `m.mu` before** snapshotting the replay, so an event broadcast in between is delivered twice. Contradicts the function's own doc comment. Window is wide for a restored session (full SQLite read + sha256 per frame).
- **MEDIUM** `hub/session.go:1540` — `run()`'s panic recover returns normally, so `detachSession`/`removeSession` never run: the session stays in `h.sessions` with a dead pump, its approvals unresolvable and its MCP token unrevoked.
- **MEDIUM** `hub/thread.go:120` — `adoptForkedSession` copies the parent's **entire** `sessionMeta` (not just project/cwd as the comment claims), so a fork inherits `worktreePath`/`repoRoot`/`port`/`fanoutGroup`. Resolving the fan-out then tears down the *kept* winner's worktree.
- **MEDIUM** `hub/turn.go:888` — the 15s nudge bound is decorative: no `agent.Nudger` implementation reads the context. While it blocks, `turnLoops` is parked and the turn can never reach `needs_you`; the wake Hold is never released.
- **MEDIUM** `hub/heartbeat.go:160` — the budget stop sends the "needs you" push twice (once via `publishVerdict`, once directly).
- **MEDIUM** `hub/hub.go:361` — `startSession` creates the worktree and reserves a port, then has `return nil, err` paths with no cleanup. Only the bootstrap failure path cleans up. The leaked worktrees are unreachable by every sweep because no session record was written.
- **HIGH** `hub/hub.go:5001` — `approval.respond` deletes the approval and decrements `pendingApprovals` **before** `m.sess.Respond` is attempted; a failed Respond leaves the approval permanently unanswerable and the card up on every device. The auto-answer path retries 3× and re-surfaces; the human path has neither.

### Providers

- **HIGH** `agent/opencode/opencode.go:975` — `message.updated` is the only SSE case that neither decodes nor filters `sessionID`, so it attributes **any** session's usage and provider errors to this one. `/event` is partitioned only by `?directory=`.
- **HIGH** `agent/opencode/opencode.go:1671` — `Respond` deletes the approval→session mapping before the POST that can fail, so the file's own 3-attempt retry sends attempts 2 and 3 to the *parent* path instead of the sub-agent.
- **MEDIUM** `agent/opencode/opencode.go:1688` — `Delete` ignores the HTTP status and returns nil for any non-2xx; the hub's "server-side delete failed" log can never fire.
- **MEDIUM** `agent/opencode/opencode.go:1542` — `sendParts` has no interlock, so with two POSTs in flight the first to return clears `turnPending` and emits `StatusIdle` for the turn the second is still running. Also resets `sawDelta`, triggering a duplicate `resyncLast`.
- **HIGH** `agent/pi/pi.go:382` — pi's `send` takes **no context at all**; every method names it `_`. `promptBounded`'s 15s deadline is ignored, so one wedged pi session stops budget enforcement, handoff indexing and stall detection for every session after it on the serial tick.
- **HIGH** `agent/claudecode/claudecode.go:588`, `agent/pi/pi.go:160` — when `readLoop` returns early on a scanner error the child is still writing, so `cmd.Wait()` blocks forever and the events channel never closes. One frame over the token cap (8 MB / 16 MB) does it.
- **MEDIUM** `agent/pi/pi.go:864` — pi's `readLoop` never checks `sc.Err()`, so a truncated stream is reported as a normal turn (or nothing). claudecode handles the identical case.
- **HIGH** `agent/cli/cli.go:316` — `stream()` blocks on Read until stdout EOF **before** `cmd.Wait()`, so a backgrounded grandchild holding the inherited stdout keeps the turn alive forever. `Probe` returns running, so the reconciler never recovers it. `orphan_test.go` misses it (its fixture keeps the parent alive).
- **MEDIUM** `agent/agui/agui.go:483` — an HTTP 200 with no terminal event is reported as a normally finished turn, so a misconfigured endpoint "finishes" instantly with an empty reply and no error anywhere.
- **MEDIUM** `genui/genui.go:276` — the `iron:ui` fence body accumulates with **no size limit**; `maxPayloadBytes` is checked only after the whole body is resident, and the over-cap body is then forwarded to clients whole as one `output.delta`.

### Issues / loops / fan-out

- **HIGH** `hub/fanout_judge.go:88` — the judge session is created but **its event pump is never started** (`go ms.run()` missing). No recommendation is ever produced; the session is ephemeral so it cannot even be opened to read one, and the provider process leaks for the daemon's lifetime. `fanoutJudgeComponent` has zero callers.
- **HIGH** `loops/loops.go:161` + `hub/hub.go:3867` — `Upsert` preserves `Handled` but not `LastRun`, and the handler never copies it, so **editing a loop resets its schedule and it fires within a minute**. Deterministic.
- **HIGH** `issues/manager.go:497` — the keep-cache loop keys on success-this-round, which a **disconnected** provider can never achieve, so `Disconnect` leaves its tickets on the board permanently. Directly contradicts `manager.go:766`. (The backoff case is correct and covered; only Disconnect is not.)
- **MEDIUM** `loops/loops.go:211` — `MaxConcurrent` is a check-then-act with the lock released across the whole spawn, so two overlapping refreshes both see `active=0` and start two agents on a loop capped at one.
- **MEDIUM** `hub/fanout_summary.go:212` — the judge spec is deleted on the first summary broadcast, so the synthesis round never gets a judge.
- **MEDIUM** `loops/loops.go:305` — a task loop whose spawn fails sets `Status:"error"` but never `Run.Error` and never logs.
- **MEDIUM** `issues/manager.go:145` — the Jira `onRefresh` closure captures its adapter's cloud id, so a refresh landing after a site switch rewrites the persisted token back to the **old** site and strands the new adapter with a rotated refresh token → `invalid_grant` → polling suspended.

### Daemon infra

- **HIGH** `main.go:368` + `hub/hub.go:809` — `SetAccounts` wires the account-env resolver onto a **one-time snapshot** of providers, and it is never called again. `agent.upsert` and `provider.refresh` both re-`Register` providers, which silently drops the wiring: sessions then spawn with the ambient environment while the Accounts screen still shows the account active. The "(idempotent)" comment at `hub.go:3546` is false for this wiring.
- **HIGH** `lsp/lsp.go:131` — on server replacement, every open document is re-announced with `textDocument/didOpen` **before** the `initialize` handshake, so the server drops them and nothing re-sends. Previously-open tabs answer nothing forever. `s.notify` only writes to a pipe so the error log never fires; `restart_test.go` asserts only map bookkeeping.
- **MEDIUM** `main.go:536` — the startup banner prints the APNs bundle from the *flag*, not the value passed to `enablePush`, so a bundle set in `~/.oculus/apns.json` is misreported to an operator debugging exactly that.
- **MEDIUM** `store/store.go:292` — `PruneSessions` never evicts orphaned `handoffs` rows despite its comment; `DeleteHandoff`'s only caller is `removeSession`, which the TTL path never reaches. Table grows monotonically.
- **MEDIUM** `hub/persist.go:77` — `persistSessionAt` has no `ephemeral` guard, so the periodic touch (and `setSessionMode`) persists scratch sessions that `addSession` deliberately refused to persist.
- **MEDIUM** `hub/hub.go:4890` — the user's message reaches the durable transcript only when `author != ""`, so an unidentified client's prompts are never persisted for pi/CLI providers ("answers with no questions").

### Swift client — silent failures

The dominant pattern: a loader uses `try?` against a capability-gated endpoint, so a
**refusal renders as an affirmative empty state**. The `*Forbidden` flag pattern already
used for accounts/remotes/MCP/devices is the fix.

- **HIGH** `ActivityView.swift:43` — a refused `activity.list` renders "No activity yet". Activity is the **default iOS destination**. There is no `activityForbidden`.
- **MEDIUM** `LoopsView.swift:158` — refused `loop.list` renders "No loops yet" plus a New-loop CTA that will also be refused. Same shape at `IssuesView.swift:211`.
- **HIGH** `LoopsView.swift:528` — the did-it-save predicate is **always true when editing**, so a failed save closes the editor and discards the changes.
- **HIGH** `IssuesView.swift:1030` — "Start agent" calls `onDone(true)` outside the `Task`, reporting success before the launch is attempted; `launchIssue` swallows every failure path.
- **MEDIUM** `IssuesView.swift:1508` — "Create" dismisses as though created when `createIssue` bails at its own guard (returns before clearing `trackerError`, so the stale-state check passes).
- **MEDIUM** `AccountsView.swift:267`, `RemotesView.swift:256`/`:301` — assigning a nil result into a dictionary **removes the key**, so a failure renders as "you never pressed the button".
- **MEDIUM** `NewSessionView.swift:1106` — iOS "add by path" clears the field before the add is attempted and routes the error to a field no visible view renders.
- **MEDIUM** `MCPServersView.swift:502` — "Test" is a silent no-op when the check request itself fails; indistinguishable from a test that found nothing.
- **MEDIUM** `SharingView.swift:211` — "Create" on an invite clears the label and reports nothing when minting fails; the only action on that screen without a did-it-move check.
- **MEDIUM** `IssuesView.swift:732` — the fallback column layout renders three of four categories, so issues normalized to `"other"` (canceled/duplicate Linear, unknown Jira status) vanish from the Kanban board while remaining in List view.

### Swift client — model / wire

- **HIGH** `OculusUI.swift:2985`/`:4698` — `nonRingFrameTypes` omits `session.heartbeat`, `activity.event` and `worktree.status`, which carry `session_id` but never enter the ring, so the `transcript.page` cursor inflates → a permanent hole in the transcript, and once inflation exceeds ring length the live window is skipped entirely. *(This set was added by the third sweep and was incomplete.)*
- **HIGH** `OculusUI.swift:3684` (and `:3673`) — `removeWorktree` erases the row, the on-device transcript cache and the auto-reopen key **before** a fire-and-forget send. The twin of the bug `stopSession` was already fixed for.
- **HIGH** `OculusUI.swift:4527` — `invokeUIAction`'s `guard let client else { return }` makes a generative-UI choice button a silent no-op *after* the card has latched to "Sent — the agent will continue." Same shape in `FormView`.
- **MEDIUM** `Protocol.swift:1522` — `WorktreePRResult` has no `error` property/key, and the result is reported only through `model.status`, which `deriveHeaderStatus` discards while connected. A failed `gh pr create` is indistinguishable from success.
- **MEDIUM** `Protocol.swift:1323` — `LoopRun` has no `error` property/key, so a failed loop run's reason is dropped and the row's only action is `onOpenSession("")`.
- **MEDIUM** `OculusUI.swift:1909` — `delegateSubtask` has no `modelProvider` parameter, so opencode receives a model id with no `providerID`.

### Swift client — chat surface

- **HIGH** `SessionSidebar.swift:834` — "Delete session" fires from a context menu with **no confirmation** and also erases the on-device transcript. The only irreversible sidebar action, while YOLO mode and "Always allow" are both gated behind dialogs.
- **HIGH** `CommandDeck.swift:349` — `Button("Edit") { onDone() }` sets `editingLoop = false`, the state it is already in. Guaranteed no-op.
- **MEDIUM** `SessionSidebar.swift:882` — `groups` (two dictionary builds, per-session regex `clean()` calls, partition, sort) is uncached and evaluated **three times per body pass**, and the body invalidates on every `@Published` mutation (~25 Hz during a turn).
- **MEDIUM** `DiffReviewView.swift:334` — the parse is cached but the add/delete **counts** are not; `totals` scans the whole diff twice per rebuild and each file header four more times.
- **MEDIUM** `ChatView.swift:3287` — the test-output pane scrolls to bottom on every line with no "is the user already at the bottom?" gate, unlike the transcript.

### Stale comments to correct while nearby

- `MCPServersView.swift:507` and `LoopsView.swift:269` claim these toggles fail silently; the model was since fixed (`OculusUI.swift:3230`, `:3621`). The comments will mislead.
- `probe_test.go`'s `deafSidecar` says the sidecar is "wedged so hard its stdin loop is gone"; the script still drains stdin. (A `muteSidecar` fixture now exists in `close_deadlock_test.go`.)
- `hub/hub.go:3546` — "Register overwrites by name (idempotent)" is false for the account-env wiring.

---

## Suggested order

1. The **capability/security** group — role demotion, `device.register`, `agent.list` env, hooks guard, http MCP passthrough.
2. The **data-loss** group — `RemoveWorkspace`, `SweepOrphans`, husk-drop JSONL.
3. **Turn identity** — `handleUnreachable` and the reconcile closure both need `stillMine(turnID)`; that helper already exists.
4. Providers, then loops/issues, then the Swift `*Forbidden` sweep (one pattern, ten sites).
