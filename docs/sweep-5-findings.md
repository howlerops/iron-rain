# Fifth sweep — findings register

Two targets, chosen because a repeat cold sweep of the whole tree is the wrong use of a pass done
without the finder fleet:

1. **The 49 fixes from the fourth sweep** — brand-new, unreviewed code, and therefore the freshest
   risk in the repo.
2. **What the fourth sweep never reached** — `daemon/relay`, `relay-cf`, `cloudflare/telemetry-worker`.

Scope is stated up front because it is smaller than the fourth sweep's: this was a targeted pass, not
a cold audit. See **Not covered** at the end for what that leaves.

---

## FIXED (5)

Commit 3dfde51. Every one has a control that fails with only that fix reverted.

| Area | What | Severity |
|---|---|---|
| `agent/cli/cli.go` | **The drain cut KILLED the process it was written to protect.** The fourth-sweep fix ends a turn when the agent exits rather than when the last holder of its stdout lets go, and it stopped waiting by CLOSING the read end. A grandchild still holding the write end then takes SIGPIPE on its next write and dies — so "an agent that backgrounds a dev server wedges its turn" became "…has it killed a second later". Now unblocked with a read deadline and handed to a detached discard-drain. | HIGH |
| `hub/session.go` | `startBuffering` CLEARED the held frames, so two overlapping prepareSubscriptions for one (session, conn) dropped the first one's — turning the duplicate the buffering prevents into a hole. And a panic between arming and draining left the subscriber muted for the life of the session. Additive now, drained under a defer. | MEDIUM |
| `hub/authlimit.go` | The throttle gate only moved forward and nothing brought it back, so long after a flood aged out, the OWNER's next mistyped code still waited the full cap. Measured at 8m16s in the control. Clamped to `now+authQueueMax`. | MEDIUM |
| `daemon/relay` (tests) | The known flake, identified: three tests dialled a host then a client with no synchronisation. Dialling only completes the upgrade; `serveHost` claims the slot afterwards, so the client could legitimately arrive first and be refused "no host for server_id". The refusal is CORRECT (the app races routes and wants a fast no; the CF relay refuses identically), so the fix is in the tests. | flake |
| `relay-cf/src/index.ts` | A low-order `sid` made `popChallenge` throw out of `fetch` — an exception surfacing as a 500 on a caller-supplied value. Now a clean proof refusal matching the Go relay. | LOW |

### One thing worth recording about method

An explicit all-zero-shared-secret check was written for the Cloudflare relay first. Deleting it
changed nothing: WebCrypto already rejects the derivation, as the Secure Curves spec requires. It was
dead code and is gone — the `try/catch` is what carries, and the test fails only when THAT is
removed. The control is what told the difference; the reasoning that produced the check was wrong
and looked right.

---

## FOUND, NOT FIXED (0) — the one entry below was closed later; kept for the reasoning

**Closed in a later commit.** The rollout-order problem was real and the answer was to remove it
rather than decide it: the key check engages only when `INGEST_KEY` is bound, so either half can
ship first with no gap. CORS came out entirely — the daemon is not a browser and never sent a
preflight, so the headers only ever enabled the drive-by writes. The key ships in a binary and is a
cost-and-noise filter, not authentication; the worker's comment says so rather than implying more.

The original entry:

- **MEDIUM `cloudflare/telemetry-worker/src/index.js` — `/ingest` is unauthenticated and CORS-open
  (`Access-Control-Allow-Origin: *`).** Anyone can POST arbitrary batches and write them into
  Analytics Engine: poisoned telemetry, and billed writes. There is no shared secret, no rate limit,
  and the 100-event cap is per request only.

  Deliberately not fixed here. Every remedy — a shared token, an install-id HMAC, a Turnstile —
  changes the contract between the daemon and a DEPLOYED worker, so shipping the daemon half before
  `wrangler deploy` silently turns telemetry off, and shipping the worker half first drops every
  daemon in the field. It needs a rollout order decided by whoever runs the deploy, not a unilateral
  code change. The impact is on data quality and cost, not on user data: the daemon already scrubs
  paths and never sends prompts, tokens or repo names.

---

## EXAMINED AND FOUND SOUND

Recorded so the next pass does not re-derive it:

- **`daemon/relay/pop.go`** — fresh ephemeral and nonce per connection, HMAC bound to both nonce and
  sid, constant-time compare, all-zero shared secret rejected by `curve25519.X25519`. The challenge
  runs BEFORE the slot is claimed, which is the property that makes it decide the claim.
- **`relay-cf` eviction semantics** — the `superseded` flag, `closePending`, and `live()` excluding
  pending hosts all hold up; a late close from an evicted socket cannot tear down its successor.
- **Backpressure** — the host reader applies backpressure after pairing and only drops before it,
  which is the right way round.

### Low, and consciously left

- Neither relay checks `Origin` (`InsecureSkipVerify: true` in the Go relay; Workers accept
  cross-origin by default). A page could register as a host for a sid it knows — but so could the
  attacker directly from their own machine, so it buys them only the victim's IP. The real protection
  is proof-of-possession, which is already there.

---

## Not covered

This pass did **not** re-audit: `protocol/`, `worktree/`, `fsaccess/`, `store/`, `crypto/`, the
`agent/*` adapters beyond the fixes reviewed, or the Swift client beyond the fourth sweep's own
changes. The fourth sweep covered those cold with 15 finder agents; this one did not, and a claim of
coverage here would be false.

If a sixth pass runs, the untouched-by-both list is the place to start: `cloudflare/`, the
`OculusUIAutomation` target, `fastlane/`, and the `site/` and `marketing/` trees.

---

## Addendum: found by the stage-5 chaos suite

**MEDIUM `agent/opencode/opencode.go` — any POST transport failure was reported as a failed turn.**
Found by the severable-proxy soak, which is the first test in this repo to cut a live socket under
the real adapter. The message POST blocks server-side for the entire turn, so on a long turn it is
the most likely thing to break; the `default` branch emitted `StatusError`, which the hub treats as
the PROVIDER declaring the turn failed. A wifi handover therefore ended a turn whose agent was still
working, explained by a raw Go transport error — stream inference beating provider truth, through
the one door the Turn Engine does not guard.

Fixed: a connection established and then broken is left to the reconciler's probe (the authority);
one never established is still reported immediately, the same refusal-versus-timeout distinction the
engine already draws. Control: with the classification reverted, no round of the soak reconnects.

Worth recording about method: the first version of `connectionBroke` checked the typed errors AND
matched message fragments for the same conditions, and deleting the typed half changed nothing — the
fragments caught everything. Emptying the fragment list instead left the soak green, which is what
identified the typed check as the load-bearing one. The fragment list is now narrowed to the two
errors `net/http` builds with `errors.New` and no wrapped cause, so both halves have teeth and each
has a control that fails.

