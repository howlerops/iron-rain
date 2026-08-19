# Cloud sessions on Sprites.dev — design

**Status:** design, nothing built. **Date:** 2026-08-19.

The goal is the Claude Code / Codex shape: one client, and you choose per session
whether the work runs on your Mac or in the cloud. Local stays the default.

## The decision that determines everything else

> **A cloud session must keep running while the Mac is asleep.**

This was the open question, and it is now answered. Everything below follows from
it, so if that requirement ever changes, this design should be revisited rather
than patched — a different answer produces a genuinely different architecture
(see "The road not taken").

Because the Mac may be off, it cannot broker credentials or relay tool calls. The
sprite has to be self-sufficient, which means it holds credentials, and that moves
the trust boundary.

## Architecture: `oculusd` runs inside the sprite

The daemon already cross-compiles clean for `linux/arm64` (verified: 18.9 MB, no
cgo, no macOS-only syscalls), and every release publishes `oculusd_linux_amd64`
and `oculusd_linux_arm64`. Running it in the sprite is a deployment change, not a
port.

The alternative — daemon on the Mac driving the sprite remotely — was rejected on
latency and volume, not taste. Every `fs.read`, LSP hover, diagnostic, worktree
operation and test run would have to be proxied, while the harnesses execute
against files that live in the VM. That is either FUSE-over-network (a latency
floor on every tool call, of which an agent makes hundreds per turn) or a daemon
split across a network boundary with approvals crossing it twice.

Running the daemon in the sprite means the approval flow, the turn engine, the
transcript, worktrees and file access all work unchanged, because they are local
to the daemon — which is local to the work.

The Mac daemon **stays**. It keeps running local sessions. A sprite session is
owned by that sprite's daemon instance, and the app talks to both.

### A sprite is just another Desktop — the fleet model already exists

`app/OculusKit/Sources/OculusUI/DesktopStore.swift:49` already owns every paired
desktop at once and connects to all of them concurrently (`connectAll`, line 138);
the switcher is built (`DesktopViews.swift:880`). A `Desktop` is
`(id: pubkey, name, wsURL, secret, relay)` and `wsURL` already accepts arbitrary
`wss://`. So a sprite running `oculusd` needs **no new multi-daemon merging code**
— the app pairs with it exactly as it pairs with a second Mac.

The one difference: a sprite has no terminal to show a QR in. The Mac daemon
creates the sprite, generates its pairing secret, starts
`oculusd serve --secret <secret>` inside it, and hands the app an `oculus://pair`
payload to add automatically.

This is the single biggest simplification in the design, and it is why the app
side is small.

### Run X on Y

| | Mac daemon | Sprite daemon |
|---|---|---|
| Local sessions | yes | — |
| Cloud sessions | — | yes |
| Agent harnesses | yes | yes (pre-installed in image) |
| Turn engine, approvals, transcript | yes | yes |
| APNs push | yes | no — see "Approvals" |
| Relay registration | yes | **no** — reachable at its own URL |
| LAN discovery, pairing QR | yes | no |
| Tracker OAuth (Jira/Linear) | yes | no |
| Wake guard (`caffeinate`) | yes | no-op |

## The trust boundary — state it, don't bury it

`daemon/hub/roles.go` records the invariant this breaks:

> "the session OWNER is whose machine and credentials the agent actually acts
> with, and it is always visible."

For a cloud session that is no longer the user's machine. Two verified facts make
this concrete rather than theoretical:

- **`daemon/crypto/crypto.go:29`** — the channel has no forward secrecy: "one
  future read of `~/.oculus/key` decrypts every session ever recorded." Sprites
  retains **5 full-disk checkpoints**. A key resident in a sprite plus one
  checkpoint leak, at any point in the future, retroactively decrypts history.
- **`daemon/worktree/finish.go:277`** — GitHub access is the ambient `gh` CLI,
  i.e. a personal `gh auth login` OAuth token: broad scope, long-lived, not
  repo-scoped.

Therefore:

1. **Cloud sessions get their own daemon identity.** Never copy `~/.oculus` into
   a sprite image. Beyond the secrecy problem, `daemon/relay/pop.go:10` documents
   host-eviction loops — two daemons sharing an identity would evict each other
   forever.
2. **"Self-hosted" and "end-to-end encrypted" describe LOCAL sessions.** A cloud
   session runs on Fly-operated infrastructure; Fly runs the hypervisor and has
   host-level access to VM memory and disk regardless of in-VM encryption. Label
   this in the UI at the point of choosing, not in a footnote.

## Credentials: nothing long-lived on sprite disk

This is the hard requirement, and the reason is checkpoints. A secret written to
disk is captured verbatim in every checkpoint taken while it was present, and
deleting the sprite does not provably delete those. **We cannot honestly tell a
user "your credentials are gone."** So they must never be there in a durable form.

| Secret | Today | Required for cloud |
|---|---|---|
| GitHub | ambient `gh` OAuth, broad, long-lived | **GitHub App, per-repo installation token, ~1h expiry, minted per session** |
| LLM provider | `~/.oculus/accounts.json`, account-wide | scoped/budget-capped per-session key where the provider supports it |
| MCP tokens | daemon registry, standing tokens | scoped short-lived, or keep that integration **Mac-only** |
| Daemon identity | `~/.oculus/key`, permanent | per-sprite, generated in place, never copied |

Inject as process env at spawn, not as files. Where a CLI insists on writing
config (`gh` does), that path must sit outside whatever the checkpoint captures —
and if it cannot, that credential is not shippable to a sprite yet.

The per-sprite Bearer token should get the lifecycle `daemon/hub/devices.go`
already gives phones: short TTL, revocable, and revocation tears down the live
connection (extend `RevokeDevice`'s `closeDeviceConns` pattern).

### What the egress allow-list does and does not do

It is reasonable defence against generic C2 traffic. It is **not** an
exfiltration control here, and we should not describe it as one: `github.com`
must be allow-listed for `gh pr create` to work at all, and a PR title, body,
branch name or the diff itself is a fine exfil channel. The sanctioned action is
the exit. Same for the LLM API domain and any allow-listed MCP integration.

Corollary: sandboxing does not retire the approval system. Firecracker isolation
bounds *local* damage; it does nothing about live credentials with network reach.
A pushed commit is not undone by deleting the VM. `setup_trust.go`'s argument —
"`sh -c` is the one thing the permission model cannot police" — does not stop
being true because the shell is disposable.

## Lifecycle

| Sprite | Daemon |
|---|---|
| create | provision image, clone repo, start `oculusd serve` |
| active | normal operation; app connects to the per-sprite URL |
| pause (~30s idle) | **VM freezes — see below** |
| resume | thaw; connections are dead and must be re-established |
| checkpoint | transparent to the daemon (hypervisor-level) |
| teardown | `~/.oculus` persists on the ext4 volume; `RestoreSessions` re-attaches on next create |

### Scale-to-zero corrupts the turn engine — fix this first

When the VM freezes, the reconciler goroutine freezes mid-sleep. On resume
`time.Now()` has jumped, so `turnQuietAfter` (30s), `turnNoProgressFor` (10min)
and `turnUnreachableWindow` (2min) all fire at once — declaring a turn stalled or
abandoned when the agent froze alongside it and is fine.

This is the same class of bug as the four fixed on 2026-08-18: a verdict derived
from elapsed wall-clock, where elapsed time is not evidence of anything. The fix
has the same shape — detect a reconciler tick where elapsed ≫ the tick interval,
treat it as "we were frozen", and reset `turnLastEvent`, `turnToolAt` and
`turnProbeSince`. Small, and it must exist before the first real cloud session.

### Approvals — smaller than first thought

The app already holds a WebSocket to every desktop, so a sprite's approvals
arrive over that connection like any other desktop's. Opening the app wakes a
paused sprite on the inbound connection and the pending approval is delivered.
**No approval relay is needed to ship**, which removes what looked like the
largest piece of new code.

What is genuinely lost without a relay: APNs push comes from the **Mac** daemon,
which knows nothing about sprite sessions. So "agent hit an approval while your
app was closed" is silent — the approval waits until you next open the app.
Acceptable for a first version, and the reason to keep a Mac-side push relay on
the roadmap rather than in the critical path.

### Prerequisite: approval rules have no execution-location dimension

**This is a live bug today, not a sprites problem.** `ApprovalRuleInfo`
(`protocol.go:1748`) matches on `Provider, Tool, Pattern, PathPrefix, ProjectID`
— and `ProjectID` identifies the *repo*, not the *machine*. `matches()` in
`approval_rules.go` has no exec-location term at all, and `authorizeMCPTool`
behaves the same way.

So an "always allow `git push`" granted while looking at a local session already
silently auto-allows the identical call on an SSH `remote.run` session
(`ExecKindSSH`, `hub.go:1619`) in the same project, with nothing telling the user
the machine changed. A global rule (no ProjectID) is worse.

Sprites raise the stakes — the same rule would authorize a tool call on rented
infrastructure — but the fix belongs to today's codebase: add an exec-location
dimension to the rule and to `matches()`, and **show where the action will run at
the moment the user grants "always allow"**. Granting a rule while looking at a
local session must not silently extend to a Fly VM.

## Provisioning blockers

None are architectural; all are image work.

- **`augmentPATH` spawns a login shell** (`daemon/main.go`) to recover nvm/homebrew
  paths. On a non-TTY container this hangs or yields nothing, and the failure is
  silent — harnesses then appear "not installed". Skip it when there is no login
  shell; bake PATH into the image.
- **`lsof` missing** from minimal images breaks `daemon/discovery` server
  detection — silently, which then starts a second `opencode`. Install it or pass
  `--opencode` explicitly.
- **Loopback-only bind** — `127.0.0.1:6000` is unreachable from outside; bind to
  the port the sprite URL fronts.
- **Pre-install** opencode, node + npm, the claude sidecar (pre-fetch its
  `node_modules`), git, `gh`, and language servers. `sourcekit-lsp` will not be
  available; Swift LSP is Mac-only.
- **Don't** pass `--relay`, `--apns-key`, or start the OAuth callback listener.

## Cost

~$0.42/active-hour (8 GB). Roughly $50–55/month at 4h/day; $150–165 for two
concurrent at 6h/day. Never cheaper than the Mac you already own — the value is
isolation, concurrency beyond one machine, Linux parity with CI, and running
while the Mac is off. Persistent-filesystem billing is by bytes written, so the
first clone + install is the expensive part and subsequent runs reuse it; that is
better than ephemeral VMs, not worse.

## Session creation only — not movable mid-flight

You choose local-or-cloud when the session starts, and it stays there. `ExecKind`
is structural: set at creation, persisted (`persist.go:46`), read back on restore.
`attacherFor` binds a session to its own agent server, and neither opencode nor
claude-code can serialise a live turn's state for transfer. Claude Code and Codex
don't offer mid-session migration either.

"Continue this work in the cloud" is therefore: push the branch, start a cloud
session on the same project. The code transfers; the conversation doesn't.

**Uncommitted local work** doesn't come along — the sprite clones from the remote.
Default to saying so plainly at session creation, with a one-tap "commit & push
current work" that reuses the existing `CommitAll` + `Push`. Do **not** build
working-tree sync: it introduces a non-git exchange and leaves the sprite holding
changes that exist on no branch.

Note the delivery path needs no new code — `worktree/finish.go`'s
`CommitAll` → `Push` → `CreatePR` are pure git/`gh` operations on a local
directory, and inside a sprite that directory is simply the sprite's. But there
is **no `git clone` codepath in the daemon today** (worktrees are always
`git worktree add` off an existing checkout), so cloning is new code.

## Staged plan

1. **Approval-rule exec-location dimension.** A live bug, independent of sprites,
   and a hard prerequisite before any rule can authorise a tool on rented
   infrastructure.
2. **GitHub App + per-repo installation tokens**, replacing ambient `gh`. Minted
   by the Mac at session start, injected as env, never written to sprite disk.
   Useful for local sessions too.
3. **Freeze/thaw clock reset** in the turn engine. Small; must precede any real
   cloud session.
4. **Sprite image + lifecycle service** (`daemon/sprites`), mirroring
   `daemon/sshremote`'s shape: injectable exec so it is testable without hitting
   Fly. Includes auto-pairing (Mac mints the secret, app adds the desktop).
5. **`ExecKindSprite`** end-to-end — create, clone, session, teardown.
6. Mac-side approval push relay; cost metering; lifecycle UI.

Rough size, given the fleet model already exists: ~800 lines daemon-side, ~200
app-side, plus the base image (ops work, and the gate on whether any of it runs).

## Mac-mediated *auth*, not Mac-mediated *execution*

The reconciliation that makes both halves work: the Mac must be online to
**start** a cloud session — it provisions the sprite, mints the scoped
credentials, and hands off. It does **not** need to stay online for the session
to keep working.

That distinction is what preserves "close the laptop, the cloud session keeps
going" without a per-call phone-home. Brokering every LLM call back through the
Mac would defeat the entire feature — that isn't a cloud session, it's
`remote.run` with extra steps.

Consequence for credentials: mint **once, at session start**, scoped and
short-TTL. A short TTL — hours, matched to expected session length — is the real
mitigation for the checkpoint problem, because "delete my data" is a vendor
promise we cannot verify. Checkpoint purge is defence in depth, not the control.

## The road not taken

If "runs while the Mac is asleep" is ever dropped, a materially safer design
becomes available: keep all standing credentials on the Mac, use the sprite as
disposable compute, and broker every credentialed action back through the
existing encrypted channel with ephemeral, single-use, task-scoped tokens. A
compromised sprite would then yield nothing beyond whatever was in flight for one
already-approved call. It is recorded here because it is safer on every axis
except the one that was chosen — and that one axis is most of the point.

## Unverified assumptions

Confirm before building:

- Whether RAM is billed while a sprite is paused (materially changes the cost model).
- Whether the egress policy is a real SNI-aware proxy or a host/IP firewall
  (decides whether domain-fronting bypasses it outright).
- Sprites/Fly's actual checkpoint retention and purge API — needed before any
  "delete my data" claim is written.
- Whether an inbound request to a paused sprite reliably wakes it fast enough for
  an approval tap to feel responsive.
