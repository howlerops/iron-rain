# Revised plan: on-device transcript cache (paint-fast, no cursor)

Repo: `/Users/jacob/projects/oculus` — Go daemon in `daemon/`, SwiftUI app in `app/OculusKit`.

## Verdict on the original plan

**The `since` cursor is cancelled. Do not build it.** Not deferred-with-a-stub — removed, with written preconditions (below) before anyone proposes it again.

One root cause, many symptoms. The plan assumed `transcript_events.seq` numbers *the replay stream*. It numbers a strict, differently-shaped subset:

- `persistDurable` (daemon/hub/session.go:570-611) stamps `m.txSeq` for exactly three cases — `TypeSessionMessage` (:578), `TypeSessionTool` with status `completed`/`error` (:589-591), `TypeSessionStatus` with `StatusError` (:593-597). `TypeOutputDelta` accumulates and returns (:599-603); everything else hits `default: return` (:604-605).
- `broadcast` (session.go:634-652) appends **every** event to the ring, and `subscribe` replays that ring (session.go:374). So `ui.component` (emitted directly via `m.broadcast` at session.go:532-539), `session.subagent`, `session.todos`, `thinking.delta`, `output.delta` and the user-prompt echo (`broadcastUserEcho`, session.go:944-951) are in the replay and can never carry a seq.
- The sets are *inverted* in places: `finalizeTurnTranscript` (session.go:617-629) persists a synthetic assistant message that is never broadcast, with an empty msg id, which `AppendTranscript` maps to SQL NULL — "else NULL → never dedups" (daemon/store/transcript.go:18-21) against the partial unique index at daemon/store/store.go:106.
- `m.txSeq++` (session.go:607) runs *before* `INSERT OR IGNORE` and discards the returned `inserted` bool, so a deduped re-replay burns seq numbers with no rows. `run()` re-seeds `m.txSeq = MaxTranscriptSeq` on every attach (session.go:723-727), and `MaxTranscriptSeq` COALESCEs an empty table to 0 (transcript.go:65-73). A cursor can **rewind below a value a client already holds**, and the plan's only reset trigger (`since < minRetainedSeq`) does not fire — silent, permanent history loss.
- The path that actually loses events on mobile — reconnect — never touches `session.subscribe`. `reopenCurrentSession` (OculusUI.swift:641-652) and `reattachCurrentSync` (:622-636) send `session.attach`, whose payload has no cursor field.

Each is individually fatal to a `seq > since` splice. Together they say the durable store is a *backstop for restart*, not a log of the conversation.

**What survives is the part users feel.** The stated problem is two things (cache-plan.md:9-11): the relay round trip, and the skeleton. Only the second is perceptible — the measured store here is 69 rows / 156 KB, so a 200-event tail is under half a megabyte and the round trip dominates. And a perfect cursor would *still* not have removed the skeleton, because `openSession` unconditionally calls `beginSettling()` (OculusUI.swift:1452) and `ChatView` renders `sessionLoadingView` while `transcriptSettling` is true regardless of how full `messages` is (ChatView.swift:82-87). The original headline claim was unreachable as designed.

So: **the cache becomes a disposable optimistic paint layer reconciled against the daemon's unchanged replay.** Zero protocol change, zero daemon change in phase 1. Every cursor-shaped objection becomes moot rather than mitigated.

---

## The design, with every open question decided

### 1. Cache raw broadcast frames, reconcile by byte equality — never by text

The client already holds the exact bytes: `receiveLoop` gets `data` from `client.recv()` before `Protocol.envelope(data)` (OculusUI.swift:2871-2874). `broadcast` hands the *same* `raw` slice to the ring and every subscriber channel (session.go:636, :647); `AppendTranscript` stores that same slice (transcript.go:14-24), which `Transcript()` returns verbatim (transcript.go:38-61). A cached frame and the same frame replayed later are **byte-identical**.

That is the whole trick. Reconciliation compares `Data`. It never touches `dedupReplay`, never trims strings, never compares roles. The worst hazard in the original plan — text-equality dedup (OculusUI.swift:2949) becoming a silent message-dropper over a pre-populated array — is designed out rather than patched.

### 2. Cursor semantics: there are none

No `since`, `seq` on the wire, epoch, `reset` flag, `MinTranscriptSeq`, or `TranscriptSince`. `session.subscribe` is unchanged. The replay is authoritative for the window it covers; the cache is authoritative for nothing.

### 3. Storage: one SQLite database on device

`~/Library/Caches/Oculus/transcript-cache.sqlite3`.

SQLite over NDJSON-per-session because the decisive requirement is a **global byte budget** — one `SELECT SUM(length(raw))` instead of N `stat()` calls plus a sidecar index. Secondary: `PRAGMA user_version` gives real format versioning a headerless append-only file cannot; eviction across sessions and daemons is one transaction; purge-on-unpair is one `DELETE WHERE daemon = ?`; and multiple `Model` instances (the app connects to every paired desktop at once — DesktopStore.swift:40, :80) serialize for free. Verified `import SQLite3` links and runs on this toolchain with no new SwiftPM dependency (`app/OculusKit/Package.swift` has none today; keep it that way).

`Caches/` and not `Application Support/`, deliberately: OS-purgeable is exactly right for a disposable optimistic cache — if iOS reclaims it we degrade to today's behaviour — and `~/Library/Caches` is already Time Machine-excluded (`tmutil isexcluded` reports `[Excluded]`), so no `isExcludedFromBackup` dance and no iOS backup inclusion.

Set `PRAGMA auto_vacuum=INCREMENTAL` at creation and run `PRAGMA incremental_vacuum` after eviction — mirroring daemon/store/store.go:44-53 — otherwise deletion never returns bytes and the budget is fiction.

### 4. Key: `(daemonPubHex, sessionID)`

Not because ids collide by chance — locally minted ids carry 48 bits of CSPRNG suffix, and the pubkey is pinned in the Noise handshake so a stale-key `Model` never connects. Three real reasons:

- Takeover/discovered sessions do **not** use a random suffix. `discovery.go` derives the id from the on-disk Claude JSONL filename, so two Macs restored from the same backup or sharing a synced `~/.claude` legitimately present identical ids.
- The cache is per-daemon state and unpair must purge it. `DesktopStore.remove(_:)` (DesktopStore.swift:132) drops the model and re-saves; nothing clears on-disk state today because there is none. That changes with this feature.
- Session ids embed user-controlled text for custom CLI agents (`cfg.Name + "_" + randID()`), so raw ids must never become path segments. With SQLite they are column values, which closes that on its own.

The codebase already namespaces per-session client state by desktop — `lastSessionKey` is `"oculus.lastSession.\(id)"` (OculusUI.swift:2053). Follow that.

### 5. What is cached: a strict allowlist

`session.message`, `output.delta`, `thinking.delta`, `session.tool`, `ui.component`, `session.subagent`, and `session.status` **only when `status == "error"`**.

The exclusions are load-bearing, not tidiness:

- `session.usage` — the handler *accumulates* (`inputTokens + u.inputTokens`, OculusUI.swift:~3123). Replaying double-counts cost on every open.
- `approval.request` — resurrects a modal sheet for a decision already made.
- `session.status` running/idle — pins a false spinner. `openSession` clears `busy` at :1448 and the replay carries the truth.
- `session.todos` — the daemon re-emits the full list constantly; `openSession` already clears them at :1446.
- `turn.state` — transient by construction (`broadcastTransient` never records it) and synthesized fresh into each replay at session.go:452-461.
- Any global broadcast (`session.list`, `issue.list`, `participants`, `activity.event`) — not session-scoped, never enters `m.transcript`.

The same allowlist is applied to the incoming replay before anchoring, so both sequences share a type universe and byte comparison is meaningful.

### 6. What paints when

**Cache hit:** `openSession` does *not* set `sessionLoading`, does *not* call `beginSettling()`, does *not* arm `dedupReplay`. It clears, sets `sessionID`, and applies every cached frame through the extracted event handler — synchronously, in one `@MainActor` tick with no `await` between. SwiftUI commits once, and `ChatView`'s `.id(model.sessionID ?? "none")` (ChatView.swift:447) builds a ScrollView already full and natively bottom-anchored on first layout. That is the entire perceived win, and it is why the paint must come from an **in-memory** snapshot: any `await` between `sessionID = id` and populating `messages` yields a render pass with the new `.id` and an empty transcript — precisely the open-at-the-top bug the settle machinery exists to hide (documented at ChatView.swift:443-446).

**Cache miss:** today's path, byte for byte — `sessionLoading = true`, `beginSettling()`, `dedupReplay` armed. No regression for cold sessions.

Hydration from disk into memory is asynchronous and happens *before* the tap: on connect for the last-opened session (`lastSessionKey`), and for the top 8 sessions by recency when `session.list` arrives. First open of a session never touched this launch is not instant — an accepted, stated limitation that still covers the two cases that matter: cold launch into your last session, and A→B→A.

### 7. Reconciliation

While `reconciling` is true, session-scoped transcript frames are buffered as raw `Data`; non-session frames are applied immediately. A 160 ms quiet debounce with a 1.5 s cap — the shape of `bumpSettle`/`beginSettling` (OculusUI.swift:1479-1498) but **without hiding the transcript** — marks the barrier. Then:

1. Set aside trailing synthesized frames (`transcript.page.end`, `turn.state`) — appended to the replay at session.go:441-446 and :452-461, not history.
2. Filter the buffer through the allowlist to get `arrived`.
3. Search `painted` backwards for the first element byte-equal to `arrived[0]`; call the index `i`.
4. **Overlap matches and is exhausted** (`painted.count - i == arrived.count`, all equal): *no-op*. Keep the painted transcript. Zero re-render, zero flicker. The common case.
5. **Overlap matches, `arrived` is longer**: apply only the buffer suffix past the overlap through the normal handler.
6. **Anchor not found, or overlap mismatches**: full replace — `messages.removeAll()`, `clearChildState()`, apply the entire buffer in order.
7. Apply the set-aside trailers. Clear `reconciling`, drain anything that arrived after the barrier.

Exact, because both sides are the same bytes from the same daemon. It degrades safely: after a daemon restart the replay is durable-sourced (synthetic assistant messages, no user echoes, no `ui.component`) and may share no bytes with a broadcast-sourced cache — anchor not found — full replace — exactly today's behaviour.

**The `anchorGuard` safety net.** For 20 s after open (longer than `replayGrace = 15 s`, session.go:32), any incoming transcript frame byte-equal to one already in `painted` triggers a full rebuild from everything received since subscribe. One mechanism covers three verified hazards: the self-replay attach window where subscribe withholds the durable transcript and the provider re-streams live (session.go:401-425); the takeover/`recoverSession` path where a claude-code attach re-emits up to 200 JSONL messages; and the `already`-branch race where the re-subscribe replay goroutine (session.go:462-478) interleaves with `broadcast` on the same `s.ch`. All three manifest as "a frame I already have shows up again".

"Show earlier messages" is disabled while `reconciling` is true.

---

## Build order

### Phase 0 — prerequisites (must land before any cache code)

Pre-existing defects the cache would make permanent or unbounded. Cheap, and not optional.

**Step 1. Extract the event switch into a method.**
`OculusUI.swift:2886-3183` — the dispatch is an inline `switch env.type` inside `receiveLoop`'s `while connected` loop. Move it to `@MainActor private func applyEvent(_ env: Protocol.Envelope, raw: Data)`. Verified mechanical: no `continue`, `return`, `throw`, or bare `await` inside the switch body (the only `await` is inside a detached `Task {}` at :2913); the `continue` at :2884 belongs to the `pendingRequests` block *above* the switch; every `break` is a switch-break. The do/catch at :2870/:3185 wraps `recv()` and `envelope()`, both above the switch.
*Test:* `app/OculusKit/Tests/OculusUITests/` — assert `applyEvent` with a synthetic `session.message` envelope appends to `messages`.

**Step 2. Fix `hasEarlierHistory` leakage and unbound page dedup.**
`hasEarlierHistory` is assigned in exactly one place — `transcriptPageEnd` at :3078 — and `openSession` (:1414-1461) never resets it, so it carries the previous session's value. Add `hasEarlierHistory = false` alongside the resets at :1446-1450.
Separately, `transcriptPageBegin` sets `dedupReplay = true` (:3066) with no time bound against a populated array, and the check at :2949 scans *all* of `messages`. With a deep painted transcript that scan grows without bound and collapses legitimately repeated rows. Scope it: when `pageAnchor != nil`, restrict the `messages.contains` scan at :2949 to `messages[pageAnchor!...]`.
*Test:* open A (truncated, `hasMore: true`), open B (no page.end), assert `hasEarlierHistory == false`. Page-load with two identical-text messages, one inside the page and one above the anchor — assert both survive.

**Step 3. Send events, not rendered rows, as the paging cursor.**
`loadEarlierHistory` sends `loaded: messages.count` (:1723) — rendered `ChatMessage`s. `historyPage` uses it as an index into a slice of **raw events**: `end := len(all) - loaded` where `all := m.fullHistory()` (session.go:694-705). The units have never matched: hundreds of `output.delta` frames fold into one row (`appendAssistantDelta`), tool events merge in place by id (:2746-2747), `ui.component` merges by id, status frames render nothing. `daemon/hub/paging_test.go` bakes the daemon's assumption in explicitly — a unit no client has ever spoken.
Add `private var daemonEventsRendered: Int` to `Model`: the number of raw daemon frames currently represented in `messages`. Set on replay, add `TranscriptPageEnd.Count` on `page.end`, increment on live transcript frames, reset in `openSession`. Send *that* as `loaded`.
This is also what makes a deeper-than-the-tail cache work: cached frames were themselves daemon events at an earlier time and occupy the same positions in `fullHistory()`, so after a splice `loaded = splicedCacheCount + replayCount` is right.
Accept the residual: `fullHistory()` is `durable ++ ring` after a restart, so positional agreement is approximate across a daemon restart. The failure mode is *overlap*, absorbed by the page anchor plus scoped dedup — same as today.
*Test:* feed 300 synthetic frames of which 250 are deltas; assert the outgoing `transcript.page` carries `loaded == 300`, not the rendered row count.

### Phase 1 — the cache

**Step 4. `TranscriptCache` (new: `app/OculusKit/Sources/OculusUI/TranscriptCache.swift`).**
A dedicated `actor` owning one SQLite handle.

```
PRAGMA user_version = 1;
CREATE TABLE frames(daemon TEXT, session TEXT, ord INTEGER, raw BLOB, ts INTEGER,
                    PRIMARY KEY(daemon, session, ord));
CREATE TABLE sessions(daemon TEXT, session TEXT, last_opened INTEGER, holed INTEGER,
                      PRIMARY KEY(daemon, session));
```

- `PRAGMA auto_vacuum=INCREMENTAL` at creation; `PRAGMA incremental_vacuum` after every eviction pass.
- On open, read `user_version`; on mismatch, `DROP` both tables and recreate. Do **not** key the version on `CFBundleVersion` — frozen at `1` in both build configurations of `app/Oculus.xcodeproj/project.pbxproj`, so a wipe keyed on it would never fire — nor on `MARKETING_VERSION`, which would wipe every cache on every App Store update, i.e. exactly when the user reopens the app and most wants an instant transcript. Bump the integer by hand when a frame shape changes.
- Set the *directory's* file-protection class to `.completeUntilFirstUserAuthentication` so `-wal`/`-shm` inherit it. Not `.complete`: the app has no `UIBackgroundModes`, no `scenePhase` observer and no protected-data plumbing anywhere in `app/`, so `.complete` is new failure surface for marginal gain — and the pairing secret two files away is already plaintext in `UserDefaults` (OculusUI.swift:275, `// TODO: move the secret to the Keychain`; `Desktop.secret` in DesktopStore.swift under `"oculus.desktops"`), and that secret grants full remote control of the developer's Mac. Encrypting the cache while the credential sits unprotected is theatre. **File moving the pairing secret to the Keychain as a separate, higher-priority ticket. It is not part of this plan and this plan does not close it.**
- Bounds as a budget on **bytes**, not a count — `SessionTool.Output` carries the full tool result and nothing in the daemon truncates it (`daemon/protocol/protocol.go:1327`; the only truncation is claude-code's 20 000-char JSONL *replay* cap, which does not apply to live events). The daemon's in-memory ring learned this and caps by both count and bytes (`maxTranscriptEvents = 2048, maxTranscriptBytes = 8 << 20`, session.go:78-80) while its SQLite table caps by rows only (`maxTranscriptRows = 5000`, transcript.go:8):
  - global ≤ 64 MB (`SELECT SUM(length(raw)) FROM frames`), evict whole sessions LRU by `last_opened`;
  - per session ≤ 512 KB, evict oldest `ord` first — head eviction only ever makes the cache *shallower*, never holed;
  - any single frame > 128 KB (well above the 14 583 B largest row observed in the live DB): drop it, set `holed = 1`, stop appending to that session for the run. A holed cache is **discarded, not painted**, on the next open. Do not truncate in place — byte identity is the correctness argument.
- **Never cache an ephemeral session.** `persistDurable` returns before any write when `m.meta.ephemeral` (session.go:571-573); `daemon/hub/ephemeral_e2e_test.go` fails the build if one lands in the store; the UI promises it in so many words — `"Ephemeral chat — no project, not saved"` (SessionSidebar.swift), `"Ephemeral — just chat, no project"` (DesktopViews.swift). The client already has the flag (`Session.ephemeral`, `Protocol.swift:684`) and already filters on it in `AllSessionsView`. Gate at the write site. Writing a scratch chat to disk would make this cache the first place in the entire system a "not saved" conversation is persisted.
- Retention: absolute TTL of **7 days**, matching `sessionTTL = 7 * 24 * time.Hour` (daemon/main.go:256), whose prune cascade exists "so the durable transcript can't outlive its session" (daemon/store/store.go:246). Match the policy users reason from, not an arbitrary 30.

*Test:* new `TranscriptCacheTests.swift` — round-trip; version bump wipes; global budget evicts the least-recently-opened session and `SUM(length(raw))` drops; per-session budget evicts oldest-first with no gap in `ord`; a 200 KB frame sets `holed` and a holed session returns nothing; an ephemeral session writes zero rows.

**Step 5. Hydration.**
`Model` gains `private var hydrated: [String: [Data]]`. Populate on `finishConnect` for `lastSessionKey`, and for the 8 most recent ids when `session.list` lands (:3084-3095). Cap ~4 MB resident.
*Test:* after a simulated connect, `hydrated` contains the last session's frames and `openSession` takes the hit path.

**Step 6. The cache-hit open path.**
In `openSession` (:1414), after the stopped-session branch at :1431-1441 and before the clear at :1444: if `hydrated[id]` is non-empty and the session is not ephemeral, take the fast path — clear, set `sessionID = id`, apply every cached frame through `applyEvent`, `flushStream()` at the end, and **skip** `sessionLoading = true` (:1451) and `beginSettling()` (:1452). Do not arm `dedupReplay` (:1427-1428).
Add a `replayingCache` flag checked in `noteActivity` (:666-672) so cached frames do not stamp `lastEventAt` or start the stall loop with fake liveness.
Filter cached frames at paint time: build the set of sub-agent ids appearing as a `session.subagent` `"started"` frame, and drop any child-addressed frame whose id is not in it. Child events route purely by `childMessages[m.sessionID] != nil` (:2967, :2985, :3019, :2752); that key is created only by `applySubAgent` case `"started"` (:2717); `clearChildState()` (:2808-2817) wipes it on every open — so an unmatched child frame is silently dropped on the floor. The filter makes that impossible rather than accidental.
*Test:* open with a hydrated cache; assert `transcriptSettling == false`, `sessionLoading == false`, `dedupReplay == false`, and `messages.count > 0` **in the same tick** as `sessionID` becoming non-nil. Assert a cache whose leading `subagent started` frame was evicted paints parent rows and no orphan child rows.

**Step 7. Reconciliation.**
Implement §7 above: `reconciling`, `replayBuffer: [Data]`, `painted: [Data]`, the 160 ms/1.5 s barrier, the anchor search, the three outcomes, the trailer replay, the 20 s `anchorGuard`. Gate `loadEarlierHistory` (:1719) on `!reconciling`.
Add self-healing for undecodable cached frames: `Protocol.envelope` only requires a `type` key and every consumer uses `try? env.payload(as:)`, so a payload-shape change silently drops the frame with no error and no log — and the daemon's own `transcript_events` table is likewise unversioned and will happily replay old-shaped bytes across upgrades. Count frames of an allowlisted type that produced no state change during a cache paint; if non-zero, force the full-replace branch at the barrier instead of splicing.
*Test:* new `ReconcileTests.swift` — (a) identical replay → array identity unchanged, zero mutations; (b) replay with three extra frames → exactly three rows appended, nothing duplicated; (c) replay sharing no bytes → full replace; (d) a frame byte-equal to a painted frame arriving 8 s after open → full rebuild fires. Plus a regression asserting a cached mid-stream partial followed by a tool card and the finalized message renders **one** assistant reply, not two — the supersede branch at :2956 requires `messages.last` to be the streaming row, and `applySessionTool` (:2749) / `applyUIComponent` (:2771) call `finalizeStreaming()` before appending, so a partial that is no longer last falls through to `messages.append` at :2962-2966. Byte-anchored reconciliation is what prevents that; this test proves it.

**Step 8. Live append.**
In `applyEvent`, after handling an allowlisted session-scoped frame for a non-ephemeral session, enqueue `raw`. **Debounced batch write on a background actor — never a synchronous write on the main actor.** `receiveLoop` runs on `@MainActor` (`public final class Model` is `@MainActor`, :32-33). Append to an in-memory buffer on the main actor; a background writer drains it.
*Test:* 500 synthetic frames produce one bounded set of writes; the main-actor path performs no file I/O (stubbed writer call count).

**Step 9. Purge and eviction triggers.**
Four explicit triggers:
- `DesktopStore.remove(_:)` (DesktopStore.swift:132) — delete every row for that `daemon` before dropping the model.
- Session deletion — `removeSession` calls `db.DeleteTranscript(id)` (daemon/hub/hub.go:1113-1118) on explicit stop, session delete and worktree remove. The client purges when *it* initiates the delete, and on a `"no such session"` error to a subscribe for an id absent from `session.list`.
- TTL sweep on connect.
- `holed` or `user_version` mismatch.

**Do not** purge by diffing incoming `session.list` against cached ids. `stoppedSessions` returns nil when `h.db == nil` or `db.Sessions()` errors (daemon/hub/persist.go:299-313), and `session.list` then degrades to live in-memory sessions only — on exactly the configuration the original plan called out as "degrade cleanly", a blind diff would wipe the entire cache on one unlucky reconnect. `broadcast` also silently drops clients whose outbound queue is full (hub.go:2122-2127), so a client can miss a list entirely.
*Test:* unpair deletes that daemon's rows and leaves the other daemon's intact; an 8-day-old session is swept; a short `session.list` leaves the cache untouched.

### Phase 2 — optional, only if the timer proves flaky in practice

**Step 10. A `replay.begin` / `replay.end` bracket on subscribe.**
`daemon/hub/session.go:372-480`. Emit through the subscriber's own channel, exactly as `sendHistoryPage` already does for paging — and for the reason its own comment gives at session.go:975-979: *"The frames go through that subscriber's own outbound channel — the same path the initial replay uses — so begin, the events, and end arrive in that order. Sending the bracket over the request socket while the events went through the subscriber queue would race."* That precedent is the whole argument. It replaces the client's 160 ms/1.5 s guess with a deterministic barrier, removes the up-to-1.5 s reconcile latency on an actively streaming session, and closes the `already`-branch interleave (session.go:462-478) properly rather than by self-healing.
Additive and backward-compatible: the client's switch ends in `default: break` (:3182-3183), so an older app ignores it.
*Test:* new `daemon/hub/replay_bracket_test.go`; extend `daemon/hub/resubscribe_test.go` — which today asserts only that the re-subscribe replay *arrives*, and nothing about its ordering against live traffic — to assert begin/events/end ordering with a concurrent `broadcast` running.

---

## Things that must NOT be done

1. **Do not add `since` to `session.subscribe`, nor `TranscriptSince`, `MinTranscriptSeq`, a `reset` flag, a seq on the wire, or an epoch token.** Preconditions before reopening it: (a) the durable set and the broadcast set are unified — `broadcastUserEcho`, `ui.component`, `session.subagent`, `session.todos` persisted, and `finalizeTurnTranscript`'s synthetic message broadcast rather than written silently; (b) `m.txSeq++` (session.go:607) only advances when `AppendTranscript`'s returned `inserted` bool is true; (c) a per-session epoch bumped in every path that deletes rows (store.go:246, transcript.go:97, hub.go:1116), stored where the `sessions`-row delete cannot take it with it; (d) the cursor is plumbed through `session.attach` as well as `session.subscribe`, since reconnect uses attach. Until all four hold, a cursor is a silent-data-loss machine.
2. **Do not truncate or rewrite a cached frame.** Byte identity with the wire is the correctness argument for the entire reconciliation.
3. **Do not cache ephemeral sessions**, or anything outside the §5 allowlist. `session.usage` in particular double-counts on replay.
4. **Do not purge from a `session.list` diff.** See step 9.
5. **Do not delete `dedupReplay`.** Still needed on the miss path, the reattach path (:624-625) and the paging path (:3066). `MsgID` cannot replace it: `omitempty` and populated only by opencode and claude-code (`daemon/protocol/protocol.go:1185-1188`), the synthetic end-of-turn row is persisted with an empty id on purpose (session.go:620-624), and the Swift `SessionMessage` does not decode it (`CodingKeys` at `Protocol.swift:1702` are session_id/role/text/author only). The cache path simply never arms it.
6. **Do not read the cache synchronously inside `openSession`.** Any `await` between `sessionID = id` and populating `messages` reintroduces the open-at-the-top bug documented at ChatView.swift:443-446. Hydrate ahead of the tap.
7. **Do not key the cache format version on `CFBundleVersion` or `MARKETING_VERSION`.**
8. **Do not claim this closes the on-device-credential exposure.** It does not.

---

## Accepted risks, stated plainly

- **The relay bytes are still paid.** Phase 1 removes perceived latency, not transfer. A 200-event tail against the measured store (69 rows / 156 KB; 2 265 B mean, 14 583 B max) is well under half a megabyte and the round trip dominates. Measure before spending the protocol budget.
- **First open of an un-hydrated session is not instant.** Bounded by top-8-by-recency hydration; cold-launch and A→B→A are covered.
- **A streaming session shows up-to-1.5 s-stale content during the reconcile window.** Better than today's up-to-1.5 s skeleton. Phase 2 removes it.
- **Paging positions are approximate across a daemon restart**, because `fullHistory()` is `durable ++ ring` and those are different event universes. The failure mode is overlap, absorbed by the page anchor. `transcript.page` remains count-based; a seq-based `TranscriptBefore` is blocked on the same preconditions as the cursor, since only durable rows carry a seq and paging today serves the ring.
- **`replayTotal` (session.go:122-124, :431) is dead state whose comment claims a stability guarantee `historyPage` does not provide** — it re-calls `fullHistory()` every page. Delete field, write and comment if you are in that file for phase 2. Not worth its own PR.
- **Every subscribe on a trimmed or cold session reads up to 5 000 SQLite BLOBs and discards all but 200** (session.go:396/:420 → transcript.go:38-61 capped at :8, truncated at session.go:434-436). Real waste, but it runs with `m.mu` released and after `sendOK`, so it stalls neither the pump nor another subscriber. Fold a `TranscriptTail(sessionID, n)` into phase 2 if you touch the file — fetch `replayTailLimit + 1` so the `truncated`/`HasMore` flag at :434-446 still works, and leave `Transcript()` unbounded because `historyPage` indexes `fullHistory()` by absolute offset.