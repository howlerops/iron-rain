# Turn Engine — making streaming truthful (design + test plan)

## Why (the symptoms this kills)

| Symptom | Today's cause |
|---|---|
| "No response" timeout while the agent is clearly still working | The **client** guesses liveness with timers (`armWatchdog` 180s/600s + `hasActiveSubAgents` + `streamMaybeStalled` 45s in `OculusUI.swift`). Any quiet-but-working stretch starves the timer. |
| Spinner forever, never completes | Turn completion is **inferred** from opencode's `session.idle` SSE event (live pub/sub, **no replay**) plus a POST-return backstop. Miss both → stuck. |
| Restart the app and the work was magically all done | The provider finished server-side; the **events** were lost in transit; restart replays state. The state was always there — nothing reconciled against it. |

Root cause: **nobody in the system authoritatively knows whether a turn is alive.** The
daemon translates provider events into a fire-and-forget stream; the client infers turn
state from event arrival timing. Every incident fix so far (idle backstops, resyncLast,
watchdog guards, turnPending abort, stall banner) is a patch on that inference.

## Target architecture (3 principles)

1. **The daemon owns turn state.** A per-session turn state machine — not event echoes —
   is the single source of truth. The client renders it and NEVER runs a timer.
2. **Provider truth beats stream inference.** While a turn is open, the daemon
   *reconciles* against each provider's authoritative state (poll/process-check), so a
   lost event can delay the truth by seconds — never lose it.
3. **The stream is gap-free.** Every event carries a per-session sequence number; the
   client keeps a cursor; subscribe carries `since_seq`. A dropped frame is re-delivered,
   never silently skipped — "did the work but the UI never saw it" becomes impossible.

## Part 1 — Turn state machine (daemon/hub)

New `turn` struct on `managedSession`, driven ONLY by the run() event pump + reconciler:

```
states: idle → queued → running ⇄ awaiting_approval → idle | error | abandoned
```

- A turn is **opened** by prompt-send (hub already write-ahead-logs prompts) with a
  `turn_id` (ULID), and **closed** only by: provider idle/done, provider error, user
  stop/abort, or the reconciler proving the provider is idle.
- Sub-agents are **children of the turn**: `turn.children[childID] = {state, title,
  lastEventAt}` (feeds today's SubAgent cards; also gives the parent turn an aggregate
  "N children running" that is part of turn state, not a client-side map).
- New protocol event `turn.state` (broadcast on every transition + heartbeat):

```json
{ "session_id": "...", "turn_id": "...", "state": "running",
  "seq": 4123, "started_at": 1699999999, "last_event_at": 1700000042,
  "children": [{"id":"ses_c1","state":"running","title":"explore"}],
  "detail": "running bash · npm test" }
```

## Part 2 — Liveness moves daemon-side (heartbeat + prober)

**Heartbeat:** while a turn is open, the daemon emits `turn.state` every 10s (even with
no provider events) carrying `last_event_at`. The client shows "working · last activity
Xs ago" — and stays patient FOREVER as long as heartbeats say the provider is busy.
A turn only becomes `abandoned` when the DAEMON declares it (below). Client timers: gone
(`armWatchdog`/`bumpWatchdog`/`watchdogFired`/`streamMaybeStalled` all deleted).

**Prober — per-provider authoritative liveness** (new `agent.Prober` interface,
`Probe(ctx) (busy bool, ok bool)`):

| Provider | Probe |
|---|---|
| opencode | `GET /session/{id}` (directory-scoped) — opencode reports the session's state; also `GET .../message` tail for the last message's `time.completed` |
| claude-code | sidecar process alive + a `{"t":"ping"}` → `{"t":"pong","busy":...}` stdio round-trip |
| pi | process alive + stdout age |
| cli | per-turn process alive (its exit already closes the turn) |

**Reconciler loop** (one goroutine per open turn, tick 15s — replaces client watchdog):
- Provider events flowing → do nothing (cheap).
- SSE/stdout quiet > 30s → `Probe()`:
  - **busy** → keep heartbeating (client stays patient — no false timeout, ever).
  - **idle** → we missed the completion event: fetch the final output (generalize
    today's `resyncLast` into `Recover() []Event` on the interface), emit it + close the
    turn. ("Restart showed the work" now happens live, within one tick.)
  - **unreachable** (N consecutive) → close the turn `abandoned` with the reason —
    the ONLY path to a "no response" UI, and it's the daemon's verdict, not a guess.

## Part 3 — Gap-free stream (seq + cursor)

- `Envelope` gains `seq` (per-session monotonic, assigned in `broadcast()`); the durable
  SQLite transcript (v0.2.103) already stores ordered events — extend it to be the
  replay-by-cursor source.
- `session.subscribe` gains `since_seq`; the daemon replays `(since_seq, now]` from
  SQLite+memory, then live. Client tracks its cursor per session and re-subscribes with
  it after ANY reconnect/app-restart — no duplicate-and-no-gap delivery.
- Client dedup (`dedupReplay` heuristics) dies; replaced by exact cursor semantics.
- Streaming deltas stay unsequenced/ephemeral (they're superseded by the finalized
  message which IS sequenced) — keeps SQLite write volume unchanged.

## Part 4 — Fanout/sub-agent aggregation

- Child lifecycle becomes part of the parent turn (Part 1), so:
  - a parent turn cannot close while a child is `running` **unless the provider's own
    idle arrives** (provider truth beats our bookkeeping — fixes "sealed too early").
  - the reconciler probes CHILD sessions too on quiet (fanout stalls become visible
    per-child: "child 2/3 stalled", not a whole-screen timeout).
- Fanout groups: `checkFanoutDone` moves onto turn-close hooks (exact, not
  status-sniffing).

## Part 5 — Test plan

**A. Unit — state machine** (`daemon/hub/turn_test.go`)
Table-driven transitions: every (state, input) pair incl. illegal ones (idle event for a
closed turn, error after close, child events after parent close).

**B. Scriptable provider sim** (`daemon/agent/agentsim`)
Grow today's ad-hoc stubs (octest, dropStub, halfOpenStub, wedgeStub) into one scriptable
provider: a scenario is a list of steps `{emit|delay|dropStream|hangPOST|exit|spawnChild}`.
Scenarios to encode (all from real incidents this month):
1. happy turn; 2. SSE drop mid-turn, idle lost (reconciler must recover output + close);
3. half-open hang (read-deadline + probe); 4. wedged interactive turn (probe busy forever
→ heartbeats keep the client patient; user abort works); 5. 10-child fanout with
interleaved approvals (children on distinct dirs); 6. child idle lost (parent closes on
provider truth); 7. provider process killed mid-turn (abandoned + reason); 8. burst of
5k deltas (seq stays gap-free under buffer pressure); 9. reconnect mid-turn with cursor
(zero duplicates, zero gaps — assert exact event list); 10. daemon restart mid-turn
(SQLite replay + reconciler adopts the still-running provider turn).

**C. Chaos e2e** (`daemon/hub/turnengine_e2e_test.go`)
Each sim scenario through the REAL hub + wire to a fake client; assert the CLIENT-VISIBLE
sequence converges to truth within a bounded number of ticks (fake clock, no sleeps).

**D. Soak — real opencode** (`daemon/cmd/oculus-e2e` extension)
Drive a real `opencode serve`: N=3 fanout with sub-agents on a big prompt; kill/restore
the network path mid-turn (drop the SSE socket via a TCP proxy); assert: no false
timeout, turn closes within 1 reconciler tick of real completion, transcript in SQLite ==
provider history. Run 10× in CI-nightly.

**E. Client invariants** (extend `ViewModelInvariantsTests`)
Feed recorded `turn.state` sequences; assert: spinner iff state==running/awaiting;
"no response" iff abandoned; busy never sticks after close; cursor monotonicity.

## Rollout (each stage independently shippable)

| Stage | Contents | Risk |
|---|---|---|
| 1 | Turn struct + `turn.state` event + heartbeat (client keeps old watchdog, ALSO renders new state — shadow mode, compare in logs) | none (additive) |
| 2 | Prober + reconciler (daemon-side); client still on old watchdog | low |
| 3 | Client cuts over: render `turn.state`, delete all client timers | medium (feature-flag: fall back to watchdog if no turn.state seen — old daemon) |
| 4 | seq + cursor subscribe; delete dedupReplay | medium |
| 5 | agentsim + chaos suite + soak in CI | none |

Compatibility: old app + new daemon = fine (extra events ignored). New app + old daemon =
feature-flag fallback in stage 3.

## Explicit non-goals
- No provider protocol changes (opencode/claude-code/pi/cli untouched externally).
- No per-token delta persistence (finalized-only stays).
- The 3h POST leak-bound stays (it's a leak guard, not a liveness signal anymore).
