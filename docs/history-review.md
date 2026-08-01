# Implementer's brief — transcript cache + replay assembly (v0.2.120..HEAD)

## 1. Verdict

**Do not ship.** This build regresses the two things it set out to fix. On the daemon side, `mergeDurableAndRing` (daemon/hub/session.go:401-411) is a set-subtraction concatenation, not a chronological merge, so *every* restored session — `ringFromStart` is false for all of them (session.go:777) — replays its durable history first and its live turn's echo, deltas and running-tool frames afterwards; with a streamed reply of ≥200 delta frames the 200-frame tail cap applied *after* the merge (session.go:459-463) slices the entire conversation off the replay, which is precisely the "empty conversation" symptom the commit claims to fix. On the client side the headline feature is unwired: `hydrateLikelySessions()` has zero call sites, so the SQLite file is write-only, cold launch never paints, and the TTL and byte budget never run — while the write path runs unconditionally, including for ephemeral "not saved" chats, which the plan lists as a must-not (docs/transcript-cache-plan.md:142, :189). Add a paint path that blanks the last assistant reply on three of four providers and a reconcile anchor that duplicates the transcript into persistent storage, and the net effect is worse than v0.2.120 on the paths users hit most.

## 2. Must fix before ship

**1. Daemon replay assembly — rewrite as one function used by both subscribe and paging.** `daemon/hub/session.go:392-475` and `:706-753`.
- *Order*: `mergeDurableAndRing` (:401-411) appends all durable then the unmatched ring remainder. Walk the **ring** instead: for each ring frame with a durable counterpart, emit the durable frames up to and including it; durable frames with no ring counterpart form the prefix. Test with ring `[ui.component, tool_running, tool_completed, assistant_msg]` — the existing tests (daemon/hub/replay_gap_test.go:117-124) all put the ring-only frame last, the one arrangement that cannot reorder.
- *Cap*: take the tail **before**/independently of the merge, or cap on persisted-class frames. Today `replay[len-200:]` (:459-463) lands entirely inside the ring-only delta tail. Regression test: 300 durable + 400 ring deltas, assert the durable `session.message` frames survive.
- *Paging*: `fullHistory()` (:706-738) was not touched and still implements the old rule (durable only when the ring is empty or `trimmed`, plus a `selfReplaying`/`replayGrace` exclusion `replayFrames` no longer applies). `historyPage` indexes it absolutely (:742-753), so after a restart with a small untrimmed ring `end = len(ring) - 200 < 0` → `nil, false` → the "Show earlier messages" button the replay just advertised (:467-472) returns nothing and disappears. Delete the divergent branches and index the same array `replayFrames` built. The stale invariant comment at :702-705 goes with them.

**2. Live traffic after subscribe is not deduped.** session.go:433-438 justifies deleting the self-replay grace window with "mergeDurableAndRing now guarantees no frame is sent twice", but the merge only sees the ring snapshot taken at :420; the provider's re-stream reaches `broadcast` afterwards. Reachable on `session.recover` (hub.go:3696-3699 subscribes before `go m.run()`) and on `session.attach` for a session `RestoreSessions` could not attach (persist.go:155-160) — and `reopenCurrentSession` (OculusUI.swift:641-652) does not arm `dedupReplay` at all. Either restore the attach-window exclusion, or hold the replay's frame-hash set on the `managedSession` for the window and drop a broadcast frame byte-equal to one already delivered to that subscriber.

**3. Cache paint blanks the last assistant reply.** `finalizeStreamingForCache` (OculusUI.swift:1778-1782) seals `streaming = false` without flushing; `flushStream` (:2662-2668) then clears `streamBuffer` without appending. Painting `[delta "hello ", delta "world"]` yields one row with empty text — verified. claude-code/pi/cli never broadcast a finalized `session.message` (the synthetic one at session.go:665-677 is written to SQLite and never broadcast), so their cached tail is always deltas. Make `finalizeStreamingForCache` do `finalizeThinking(); flushStream(); cancelFlush()` before sealing (or widen `finalizeStreaming()` to internal and call it). Add `XCTAssertEqual(m.messages.last?.text, "partial")` to TranscriptCacheTests.swift:170-180 — that missing assertion is what let this ship.

**4. Wire the read/eviction half.** `hydrateLikelySessions()` (ModelTranscriptCache.swift:70) and `forgetCached(_:)` (:224) have zero call sites; `TranscriptCache.frames()` and `sweep()` are reachable only through them. Call `await hydrateLikelySessions()` in `finishConnect` after `loadSessions()` (OculusUI.swift:424) and before `reopenCurrentSession()` (:437); call `forgetCached(id)` from `stopSession` (:735) and `removeWorktree` (:2253). Move `sweep()` off the hydration hook onto its own connect path so the 7-day TTL and 24 MB budget (TranscriptCache.swift:34, :39) survive a future refactor. The test must drive the connect path, not seed `transcriptHydrated` by hand.

**5. Ephemeral sessions are written to disk.** No `ephemeral` reference exists in ModelTranscriptCache.swift or TranscriptCache.swift. Gate at the write site (`captureFrame`, ModelTranscriptCache.swift:190) *and* in `TranscriptCache.append` — a lookup through `sessions` races `session.list` for a brand-new chat, so keep an `ephemeralSessionIDs` set seeded from the `Session` already in hand at OculusUI.swift:1618. Add the plan's test: an ephemeral session writes zero rows.

**6. Reconcile anchor uses `lastIndex(of:)`.** ModelTranscriptCache.swift:153. Byte-identical `output.delta` frames (a newline, a token) repeat constantly, so a shorter tail match satisfies the guard at :155 and the splice re-applies real content — which is then appended to `transcriptPainted` and written to SQLite (:167-168), compounding on each open (reproduced: 4 → 6 → 8 frames). Search for the **maximal** overlap: iterate `i` from `max(0, painted.count - arrived.count)` upward, take the first `i` where `painted[i...] == arrived[0..<(painted.count - i)]`, else full-replace.

**7. Re-attach re-captures the whole replay.** `receiveLoop` calls `applyEvent` then `captureFrame` unconditionally (OculusUI.swift:2957-2958), and `reopenCurrentSession` (:641-652) clears `messages` but leaves `transcriptPainted`, `daemonEventsRendered`, `transcriptReconciling` and `transcriptAnchorGuardUntil` intact. Every reconnect (including the user-facing Reconnect button, :1556) appends ~200 duplicate frames to the painted array and to SQLite; `finishReconcile` anchors on the second copy and cannot repair it. Reset the `transcript*` state in `reopenCurrentSession` and `reattachCurrentSync` the same way `openSession` does at :1453-1463, and reset `transcriptAnchorGuardUntil` in `openSession`.

**8. The reconcile barrier fails open.** `armReconcileCap` (ModelTranscriptCache.swift:128-135) starts at paint, before `session.subscribe` is even sent (OculusUI.swift:1470 vs :1479); `finishReconcile` clears `transcriptReconciling` *before* the empty-buffer guard (:145 vs :149) and nothing re-arms it. On a slow link the whole replay then renders below the painted copy with `dedupReplay` deliberately disarmed (reproduced: 3 rows → 5). Do not clear the flag when the buffer is empty; start the cap on the first buffered frame; arm `dedupReplay` on the hit path as a backstop. Note also that the plan's `anchorGuard` (docs:94) was specified as "triggers a full rebuild" but implemented only as a capture skip (ModelTranscriptCache.swift:196) evaluated *after* `applyEvent` — it protects nothing on the render path.

**9. The paging cursor is wrong in three directions.** All in OculusUI.swift.
- Double count: page frames increment at :2982 *and* `page.end` adds `e.count` at :3174 → net `2 × len(page)`, so the second tap skips an entire page of history permanently.
- Over count: `run.output`/`run.result`/`session.heartbeat`/`activity.event` carry `session_id` but go through `h.broadcast` and never enter `m.transcript` — one test run drives `loaded` past `len(all)` and kills the affordance. Replace the 3-type denylist at :1753-1755 with an allowlist mirroring `managedSession.broadcast`.
- Inverted dedup window: :3051 scopes the scan to `messages[anchor...]` — the page's own rows — when `pageAnchor` marks the boundary the other way. Use `messages[startIndex..<min(anchor, count)]`. (Scoping alone is necessary, not sufficient; the undercount from cache-painted frames is the root cause.)

## 3. Should fix soon

- **`busy` pinned by a cached `thinking.delta`** — OculusUI.swift:3079 sets `busy = true`; `session.status` idle is not cacheable and a durable-sourced replay carries none, so an idle restored session shows a spinner, disables Recover/Restart, and fabricates "stream may be stuck" after 45 s. Clear `busy` + `stopStallLoop()` at the end of `paintFromCache`, and add the plan's `replayingCache` flag to `noteActivity`.
- **`session.subagent` can never be cached** — `SubAgent` has no `session_id` (Protocol.swift:598-604), so the guard at ModelTranscriptCache.swift:37 always fails and the entry in `cacheableTypes` (:32) is dead. The card vanishes from a paint and is re-applied at the bottom of the transcript; the full-replace branch's `clearChildState()` (:173) then destroys child state the unbuffered frames built. Attribute by `parent_id`, or remove the type and document that sub-agent history is not cached.
- **`transcriptHydrated` has no bound** — written at OculusUI.swift:1446 on every switch, never evicted (its only remover is the uncalled `forgetCached`). The plan's "~4 MB resident" (docs:148) is unimplemented, and `transcriptPainted` is uncapped in memory (600 is the *disk* cap).
- **`PRAGMA auto_vacuum=INCREMENTAL` after `journal_mode=WAL` is a silent no-op** — TranscriptCache.swift:54 vs :58; measured `auto_vacuum = 0`, so `vacuum()` (:271) reclaims nothing and unpair leaves deleted payload bytes readable in the file, contradicting DesktopStore.swift:137-139. Move the pragma above WAL, add `PRAGMA secure_delete=ON` and `wal_checkpoint(TRUNCATE)` after purge, and run a one-time full `VACUUM` when the pragma reports 0 (a schema bump does *not* repair an existing file).
- **`persistDurable` can wedge permanently** — session.go:650-658 advances `txSeq` only on `inserted`, but `INSERT OR IGNORE` returns false for both a `msg_id` dedup and a `PRIMARY KEY(session_id, seq)` collision. After a two-binding race (`session.recover` has no already-live guard) the loser retries the same occupied seq forever, silently. Distinguish the two conflicts, re-seed from `MaxTranscriptSeq` and retry; make `finalizeTurnTranscript` (:671) obey the same rule.
- **`loadEarlierHistory` is not gated on `!transcriptReconciling`** — OculusUI.swift:1787; plan step 7 required it. A tap inside the ≤1.5 s window buffers the page's events into the replay buffer and appends the oldest history at the bottom. One-line guard plus `.disabled(model.transcriptReconciling)` at ChatView.swift:416.
- **A short replay wipes a deeper cache** — the containment test at ModelTranscriptCache.swift:155 cannot express "replay is a prefix of painted", so a 200-frame replay against a 600-frame cache takes the destructive branch and `TranscriptCache.replace` (:181) rewrites the file to the shallower set. Only call `replace` when the barrier closed on the quiet timer, not the cap.
- **`subscribe` no longer snapshots and registers atomically** — session.go:478-485 registers, unlocks, then `replayFrames` re-locks (:419-421) and reads SQLite (:446). A frame persisted-then-broadcast in that window lands in both the replay and the live queue. The doc comment at :373-377 still asserts the atomicity the code lost.
- **`replay-probe` proves less than it is quoted as proving** — daemon/cmd/replay-probe/main.go:152-159 counts wire frames and hashes raw bytes, so `visible` is not rows (tools/UI merge by id) and `duplicated=0` is tautological for the one mode the merge removes. It cannot see the synthetic-vs-JSONL assistant pair or a post-barrier replay. Stop citing it as a rendering result; the missing measurement is a Swift test driving `Model.applyEvent` over a captured replay fixture and asserting row count and order.

## 4. The long-term rule

The reviewed rule is wrong in three ways: it defines the replay as a *set* (byte-subtraction) when the client renders a *sequence*; it claims a no-duplicate guarantee that covers only the ring snapshot, not traffic broadcast after registration; and it states the rule in `replayFrames` while `fullHistory` — which paging indexes — still implements the old one. Corrected:

```go
// replayFrames assembles what a subscriber is sent: this session's history in BROADCAST ORDER,
// oldest-first, capped to the tail.
//
// SOURCE. The in-memory ring holds everything broadcast by this process; the durable transcript
// (SQLite) holds a strict, differently-shaped subset — finalized messages, completed/errored tool
// cards, error markers — that survives restarts. The ring is the whole story only for a session this
// process created and never trimmed (ringFromStart && !trimmed). Otherwise the two must be joined.
//
// ORDER IS PART OF THE HISTORY, NOT A DETAIL. The ring carries frames the durable store never sees:
// the user-prompt echo, output/thinking deltas, ui.component, session.subagent, and tool cards in
// their `running` state. Those frames are only meaningful in position — a `running` card emitted
// after its `completed` twin reverts a finished tool to a spinner, and deltas emitted after their
// finalized message render the reply a second time. So the join walks the RING and emits each
// durable frame at its ring position; durable frames with no ring counterpart (older than the ring
// window) form the prefix. Never concatenate source-by-source.
//
// NO FRAME TWICE, INCLUDING AFTER REGISTRATION. Matching is by exact bytes, because both sides ARE
// the same bytes (broadcast hands one slice to the ring and the store keeps it verbatim), and the
// match is count-aware so a genuinely repeated event survives as two. Byte identity holds only
// WITHIN one process: a synthetic end-of-turn message (finalizeTurnTranscript) and the same reply
// re-streamed from a provider's own history after a restart are different serialisations of the same
// text and will BOTH be emitted — dedup those on (type, role, normalized text), not bytes.
// The snapshot is taken under the same lock hold that registers the subscriber, and the frame set
// delivered in this replay is retained for the attach window so a provider re-stream arriving as
// live traffic is dropped rather than shown twice.
//
// THE CAP PRESERVES MEANING. The tail limit is applied so that it cannot consume the replay with
// frames that carry no standalone meaning: cap the ring window first (or cap on persisted-class
// frames), then join, then bound. A replay that contains no session.message is a bug, not a tail.
//
// ONE ASSEMBLY, ONE COORDINATE SPACE. subscribe and transcript.page index the SAME array this
// function returns. `loaded` counts frames of this array; anything the client counts that never
// entered it (hub-wide broadcasts) or fails to count that did (child-addressed and sub-agent frames)
// makes paging skip or repeat history. If this rule changes, historyPage changes with it in the same
// commit.
```

## 5. What was not covered

- **macOS.** Every memory/eviction argument assumed iOS jetsam and a purgeable `Caches/`. On macOS `~/Library/Caches` is not reclaimed by the OS, and `DesktopStore` creates one `Model` per paired desktop (DesktopStore.swift:144-148), each with its own unbounded `transcriptHydrated`, all sharing one `TranscriptCache` actor — aggregate disk and RAM across desktops was never bounded or measured.
- **Multi-device divergence.** Two clients on the same daemon keep independent caches and independent `daemonEventsRendered` cursors; nothing checked that a session mutated from device A reconciles correctly when device B paints its own stale cache. The `already`-branch re-replay (session.go:502-517) makes this concrete.
- **Format migration in the field.** `schemaVersion` is a hand-bumped constant with no upgrade test; and the plan's step-7 self-healing (count allowlisted frames that produced no state change during a paint, force full replace) was never implemented — so a payload-shape change silently paints a transcript with holes and `Protocol.envelope` reports nothing.
- **Whether the feature delivers the win.** No end-to-end measurement of paint time, replay round trip over the relay, or the 1.5 s cap against real relay latency — the single variable deciding whether the barrier races. The plan's own accepted risk (docs:200) says the round trip dominates; nobody measured it.
- **Cross-actor write serialization.** One shared `TranscriptCache` actor serializes writes for all desktops; a slow SQLite write during a streaming turn stalls every `Model`'s flush. Never load-tested.
- **The security ticket the plan explicitly deferred.** Cached tool output now puts repository source on the device under `.completeUntilFirstUserAuthentication` while the pairing secret remains plaintext in `UserDefaults` (docs:137). This diff increases what a device compromise yields; the Keychain ticket is still unfiled.