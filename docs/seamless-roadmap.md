# Roadmap to Seamless

## Where we are

The hard parts are built and working: genuine shared-control opencode takeover, a daemon-authoritative Turn Engine with no client death-timers, a durable SQLite transcript with a sound replay join, LAN+dual-relay racing with E2E encryption, and a worktree story that exceeds Claude RC's `--spawn`. The premise fails not in the core loops but at the **seams** — the exact moments the premise names. "Swap to my phone from anywhere" dies when the Mac sleeps (nothing prevents or diagnoses it), when the phone foregrounds (no lifecycle hook, no keepalive, up to 15s of frozen backoff), when the daemon's relay registration goes half-open (no ping on either end — unreachable for minutes while running fine), and when the daemon restarts (taken-over sessions reopen empty or in the wrong cwd; pi/CLI history loses the user's half). The single biggest obstacle is **liveness at the swap moment**: every layer of the transport assumes the other end is alive and has no way to learn otherwise. Until dead sockets are detected in seconds instead of minutes-to-never, everything downstream — instant paint, honest status pills, takeover — is painting on a connection that may not exist.

---

## Phase 0 — One-day fixes (ship this week)

No phase structure justifies sitting on these; each is hours of work against a confirmed defect.

- **`claude_code` system prompt preset** — one line in `daemon/agent/claudecode/sidecar/sidecar.mjs:156-160` (`options.systemPrompt = { type: "preset", preset: "claude_code" }`); `refreshSidecarIfStale` (autodetect.go:301-314) auto-ships it. Today every claude session literally is not Claude Code.
- **Promptless create seeds idle** — `seedStatus(StatusIdle)` in `startSession` when `req.Prompt` is empty (hub.go:379-382), killing the fabricated "Live on empty conversation".
- **Usage replay filter** — exclude `session.usage` from `replayFrames` (session.go:535-538); the OK-reply totals are authoritative. Kills the 2x cost meter.
- **First reconnect attempt at 0s** — attempt, *then* back off (OculusUI.swift:485-489).
- **`persistedMeta` gets `cwd` + server `URL` on attach** — `hub.go:3671` currently persists `sessionMeta{}`. This is the data half of Phase 3; write the fields now so sessions attached *this week* are recoverable when Phase 3 ships the restore logic.
- **approval.resolved into the ring** — route through `m.broadcast` instead of hub-wide-only `h.broadcast` (hub.go:3636), so a mid-turn hop can't resurrect an answered approval.

**Verify:** create a session with no prompt → sidebar shows idle. Open a long-running session twice → cost chip identical. Answer an approval on the Mac, then subscribe from the phone mid-turn → no phantom card.

---

## Phase 1 — The swap moment never fails silently

**Promise: pull out your phone anywhere, and within ~3 seconds you either see your live session or a truthful reason you can't.**

This attacks all four liveness holes at once, because they compound: a stale relay registration behind a suspended app behind a sleeping Mac is indistinguishable from "broken" today.

1. **Daemon↔relay keepalive.** WebSocket ping every ~25s with pong deadline on the registered host socket; tear down and re-dial on failure so re-registration happens in seconds, not at TCP-keepalive timescales. (`daemon/main.go:421-438`, `daemon/wsmsg/wsmsg.go:53-56` — reads currently unbounded.) On the CF DO, `ctx.setWebSocketAutoResponse` so pongs preserve hibernation (`relay-cf/src/index.ts`).
2. **CF DO gets the Go relay's semantics.** One host, one paired client, role-routed frames — never broadcast to all sockets (`relay-cf/src/index.ts:69-75` currently corrupts a live bridge when a second device races in). No registered host → close immediately with a policy code the app maps to `.unreachable` (kills the 12s padding on every offline determination; contrast `daemon/relay/relay.go:153-168`).
3. **App lifecycle + keepalive.** Observe `scenePhase` in `OculusMain` (zero lifecycle wiring exists today): on `.active`, cancel any pending backoff sleep and `attemptConnect` immediately; if "connected", send a cheap probe (identifySelf with short deadline) and force-reconnect on failure. Add `URLSessionWebSocketTask.sendPing` every ~20s so an idle-session dead pipe is detected within a minute instead of never (`OculusUI.swift:481-494`, `Client.swift`).
4. **Mac sleep: mitigate and diagnose.** Daemon takes an `IOPMAssertion` (or `caffeinate -s` child) while any turn is open or any remote client is connected, at least on AC power. App-side: when all routes fail but the desktop was recently seen, say "Your Mac may be asleep — plug it in or disable sleep" instead of the generic string at `OculusUI.swift:407`.

**Verify (walk it, don't unit-test it):** (a) Background the phone for an hour with the session idle, foreground it → connected-and-current within 3s, no stale "Connected" lie. (b) Sleep/wake the Mac while the phone is elsewhere → phone reconnects within ~30s of wake without user action. (c) Close the lid mid-turn on battery → phone says the Mac is asleep, not "Reconnecting…" forever. (d) Open the iPad while the iPhone is on the CF relay → both stay connected. (e) Kill the daemon → phone reports unreachable in under 2 seconds.

---

## Phase 2 — Reconnect paints instantly and the UI stops lying

**Promise: the moment you swap, you see the transcript you left, the stream continuing, and status pills that match reality.**

1. **Kill the reconnect blank.** `finishConnect` runs identifySelf + loadSessions + reopenCurrentSession first, fires the other ~10 loads concurrently after (`OculusUI.swift:414-440` — 13 sequential relay RTTs today). `reopenCurrentSession` paints in place: keep `messages`, seed the painted set, reconcile against the replay byte-exactly — `finishReconcile` already exists, only the call is missing (`OculusUI.swift:644-659`).
2. **Status pills track truth.** Broadcast a compact per-session state event on every turn open/close edge; client folds `session.status`/`turn.state` into `sessions[i].status` (today: frozen snapshots, `SessionSidebar.swift:598`, `OculusUI.swift:3106-3161`).
3. **Turn Engine verdicts write back.** `closeTurn` sets `m.lastStatus`, records an activity event (NeedsYou on abandoned), fires `pushAgentError`, broadcasts (`daemon/hub/turn.go:131-175` — today an abandoned agent stays "running" forever and nobody is told it died).
4. **Needs-you clears itself.** On approval respond and error→recovery, mark the session's unread NeedsYou events read and broadcast the feed (hub.go:3596-3637; reuse the supersede pattern at activity.go:114-122).
5. **The session hops with you.** Add `focusedSessionID` to identify/participants presence; on connect, if another device has a focused session with an open turn, land there or show a one-tap "Continue *fix-auth-bug* (running on MacBook)" banner (`OculusUI.swift:2130` per-device lastSession is the wrong landing point).
6. **Small honesty fixes:** split `model.status` into connectionState + sessionStatus (ChatView.swift:627-632); heartbeat idle branch + snapshot-on-connect (heartbeat.go:268, 288); warm-cache mid-turn open keeps the last row streaming instead of splitting the reply (ModelTranscriptCache.swift:280-285 + turn snapshot); drafts persisted to UserDefaults minimum, `session.draft` broadcast if cheap.

**Verify:** Drive session X from the Mac all morning; open the phone → it lands on (or offers) X, transcript paints instantly from cache, live deltas splice into the same bubble, sidebar pill says running. Let the reconciler abandon a turn → push arrives, pill flips, no immortal "Live". Approve on Mac → phone badge clears itself.

---

## Phase 3 — Takeover keeps its promise past the first hour

**Promise: a taken-over terminal session behaves like an owned session — it survives restarts, tells you what carried over, and gives you a way back to the terminal.**

1. **Attach survives daemon restart** (the worst confirmed break — a 6-hourly self-updater guarantees users hit it): finish what Phase 0 started. `attacherFor` passes the persisted URL to the factory (persist.go:336-350, main.go:220-225); opencode restore treats `resolveDir` failure as attach failure → stopped/restartable, never subscribe-blind-and-reopen-empty (opencode.go:263-272, 350, 489-521); claude restore resumes with the persisted cwd so the sidecar edits the *right project* (claudecode.go:127-129).
2. **Fork with a way back.** Expose the real claude UUID in session.info; add "Continue in terminal" that copies `claude --resume <uuid>` (the daemon already holds it — claudecode.go:49-97; zero UI references exist). Warn-and-confirm on taking over a Live row, naming the in-flight-turn loss and concurrent-writer risk.
3. **No impostor duplicates.** Claude provider exposes `ManagedUUIDs()` from the resume map; discover drops or badges "already managed" rows (discovery.go:278-284, hub.go:3644-3650 dedup is exact-id only and never matches cc_… ↔ UUID).
4. **State transfer is stated, and real where cheap.** On opencode attach, read the last assistant message's model and seed `s.modelID` (opencode.go:961-981 silently switches models today); replay tool rows in both attach paths; a "continuing with model X" line in the header.
5. **Takeover becomes proactive.** Auto-discover on connect; live discovered sessions render as a "Continue from terminal" strip atop the session list (discover is buried 4 taps deep and pull-only today — hub.go:3720-3734, SessionSidebar.swift:603-604). Fallback Live signal for claude (running process + JSONL mtime) so the unverified `claude agents --json` dependency degrades visibly, not silently (discovery.go:215-239).
6. **CLI/pi restart amnesia:** persist `hadTurns`; restart uses `ResumeArgs` when configured (cli.go:184-198, persist.go:170-231). Add pi transcript discovery + resume-Attach mirroring the claude pattern.

**Verify:** Take over a terminal opencode session, work from the phone, `kill` the daemon, let launchd restart it → the session reopens with full history and sends still land in the terminal's server. Take over a claude session, converse on the phone, sit at the Mac, run the copied `claude --resume` → the phone conversation continues in the terminal. Scan while a daemon-started claude session runs → no duplicate row.

---

## Phase 4 — Single-source transcript (the ring-demotion refactor): **accepted, here**

**Promise: history is complete and identical on every device, after any restart, for all four providers — including your own prompts.**

This is the known candidate, and it belongs in Phase 4 — not earlier, deliberately: the missing-user-half hole only *manifests* after a daemon restart (worst for pi/CLI), while Phases 1–2 fix failures that occur on **every single swap**. It must not come later either, because Phase 3 makes restarts a first-class survivable event, which makes the incomplete-replay hole the next thing users hit.

- **Stage 1 (daemon-only, independently shippable):** persist the full render set — user echoes (mint a hub-side msg id; the txSeq single-writer invariant at session.go:116-120 breaks because echoes append from the hub goroutine — guard under `m.mu` or funnel through the pump), `ui.component`, `session.subagent`; broadcast the synthetic end-of-turn message instead of silently writing it (deletes `isSyntheticAssistantEcho`, session.go:444-461). Deltas stay out of SQLite. Per-class caps on `maxTranscriptRows`.
- **Stage 2:** stamp frames with turnID (already on managedSession) and demote the ring to *open-turn delivery buffer*: replay = durable(closed turns) ++ ring(current turn). This **deletes** `joinHistory`'s hash-positional matching — the machinery that produced the five history bugs — makes cache reconciles trivially sound, and satisfies the preconditions for a future since-cursor delta reconnect over the relay. Fold in the subscribe/snapshot atomicity fix (session.go:570-582 one-frame dup window) while that code is open. Keep: ephemeral ring-only branch, the replay-dedup window for self-replaying providers.

**Verify:** In a pi session, ask three questions, restart the daemon, reopen → the full dialogue including *your* messages, gen-UI cards, and sub-agent rows. Diff the transcript rendered on two devices byte-for-byte. Reconnect ten times in a row → zero duplicate rows.

---

## Phase 5 — Fresh sessions and the worktree loop closes from the phone

**Promise: idea → working agent in two taps; finished agent → merged and cleaned up without touching the Mac.**

1. **Prompt-first creation:** a "What should the agent do?" field in NewSessionView passed as `SessionCreate.prompt` — the daemon path exists and is used by issue.launch/fanout; the primary sheet bypasses it (OculusUI.swift:866 sends `prompt: nil`). The agent works during bootstrap instead of after you re-engage.
2. **Memory in the sheet:** persist last project(s) + worktree toggle; `useWorktree` defaults ON for git repos (NewSessionView.swift:17-18 re-zeros every open); "Start like last time" in the palette.
3. **Honest branch base:** show current branch + ahead/behind on project rows; default to fetch + `worktree add origin/<default>` when the checkout is behind or off-default (worktree.go:90-99 silently branches from week-old HEAD).
4. **Close the finish loop:** PR URL persisted on session meta and tappable in WorktreePanel (today it's transient status-bar text, OculusUI.swift:3018-3019); poll `gh pr view --json state` on the conflict-sweep cadence → "PR merged — clean up worktree?" push wired to existing `worktree.remove`; local merge finish for no-remote repos guarded by `WouldConflict`; "Commit & retry" on the dirty-tree catch-up dead-end (finish.go:226-230); `createPR` gets the `workspacePRRunning` treatment (busy flag, disabled button, inline result).

**Verify:** From the phone, cold: open app → New session → type the idea → Start (two decisions max) → agent is already working when bootstrap finishes. Days later: PR-merged push arrives → tap → worktree cleaned up. Never open the laptop.

---

## Phase 6 — Trust hardening and reach polish

- **Per-device credentials:** enroll each device's static pubkey at pairing (Noise already proves identity), Devices list with revoke, `oculusd rotate-secret` fallback (today: one permanent shared secret, no revocation — main.go:147-149).
- **Silent push pre-warm:** `content-available` background push on turn completion; `PushDelegate` connects, drains the transcript delta into the cache, exits (push.go:141 is alert-only).
- **Branded relay domains** (relay1/relay2.ironrain.dev) with workers.dev as third fallback; per-relay health in status detail (main.go:67 is a personal workers.dev subdomain).
- **Reboot-survival prompt:** offer the LaunchAgent the first time a *remote* device pairs, default on; "survives reboot: yes/no" in status; document the FileVault login-screen limitation (LoginItemManager.swift:4-11).
- **Non-Prober honesty:** cheap subprocess Prober (process alive + output-pipe write age) or "quiet for Nm" in turn.state detail (turn.go:264-266).
- **Cache hygiene:** wire the orphaned `forgetCached(_:)` into stopSession/removeWorktree; verify purge-on-unpair (ModelTranscriptCache.swift:250 has zero call sites).

---

## What we do NOT build

- **Multi-client live attach for claude-code.** The SDK doesn't support it (sidecar.mjs:182-196 admits it). We ship the fork-with-handback (Phase 3) and, only if takeover telemetry demands it, a *read-only* JSONL live mirror with a "fork to steer" button. We do not reverse-engineer a two-way bridge.
- **Terminal discovery/adoption for generic CLIs (tmux/PTY pane takeover of aider/codex/cursor).** Enormous surface, fragile per-tool, low usage. Instead: pi gets real resume-based takeover (its session files are on disk), CLIs get restart continuity via ResumeArgs, and the UI/marketing scope the takeover claim to opencode + claude + pi honestly.
- **Cloud-stored transcripts / server-side sync service.** The relay stays a ciphertext pipe — that E2E property is a competitive moat vs. Claude RC, and Phase 4 delivers the same user-visible outcome (lossless multi-device history) with the daemon as the single source.
- **A session cap, quota, or "headless mode".** No cap exists (hub.go:1062-1086) and none is needed; adding RC's 32-session ceiling would be building a limitation.
- **Draft sync beyond last-writer-wins.** No CRDTs, no operational transforms for a composer text field. Debounced LWW broadcast or nothing.
- **A web client or any second app platform.** Every seam above is unfixed engineering on the platforms we have; a new client multiplies every one of them.
- **Rebuilding what the audit confirmed works:** the replay join, the Turn Engine, relay racing, worktree bootstrap, fan-out, the approval hop. The roadmap above touches these only at their confirmed defect sites.