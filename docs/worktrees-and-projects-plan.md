# Plan: Projects, worktree automation, and multi-desktop

Three tracks: (1) **Projects/folders** — spawn sessions scoped to a folder; (2) **Worktrees** —
one isolated branch per session, reusing the local harnesses; (3) **Multi-desktop** — the iOS app
pairs with several Macs, named + grouped.

## What competitors converged on (research)
**Table stakes** (Conductor, Crystal, Vibe Kanban, Cursor, Uzi, Claude Squad, Sculptor, container-use):
1. Register a project = point at a git repo.
2. One-click "New session" → `git worktree add` on a fresh branch automatically.
3. Isolation = own working dir + branch (git enforces one-branch-per-worktree).
4. Live agent output.
5. Diff / PR review before merge.
6. Cleanup: archive/remove worktree after merge (Cursor auto-cleans: 6h interval, 25 max/machine).
7. Archive/history you can restore.

**Best ideas worth stealing:**
- **Setup hooks** (Cursor `.cursor/worktrees.json`, Conductor "files to copy" + scripts, Uzi `uzi.yaml`):
  per-worktree bootstrap (npm ci, copy `.env`, init DB). This is what makes worktrees actually usable.
- **Port auto-assignment** from a range (Uzi `portRange` + `$PORT`) — prevents the `:3000` collision.
- **Human-readable workspace names** (Conductor uses city names) over hashes.
- **AI-assisted merge-conflict resolution** (Conductor `/resolve merge conflicts`).
- **Checkpoint-rebase** finish flow (Uzi `uzi checkpoint` = commit + rebase worktree back to base) —
  cleaner than a full PR for local work.
- Container isolation (Sculptor, Zed+container-use) is the heavyweight option — out of scope for v1
  (we reuse local harnesses, not Docker), but note it as the future "hard isolation" tier.

**Top 5 pitfalls to design around:**
1. **node_modules per worktree** — disk + install time explode (500MB→5GB for 10 agents). Symlinking
   deps is *unsafe* (stale/broken main tree). Mitigate with setup hooks + pnpm/APFS copy-on-write guidance.
2. **DB migrations collide** across parallel agents on one DB → per-worktree DB / `.env.local` / schema-per-branch.
3. **Git hooks are shared** (`.git/hooks` is common across worktrees) → offer a "skip hooks for agents" toggle.
4. **Gitignored files not materialized** (`.env`, `.venv`, certs, node_modules) → "files to copy" + setup hook.
5. **No cross-worktree conflict warning** — two agents editing `src/utils.ts` on different branches diverge
   silently → surface a "shared files" overlap warning across active worktrees (later).

**Architecture split (endorsed by the research):** the Go **daemon owns the worktree lifecycle**
(create/bootstrap/monitor/cleanup); the **native app owns UX** (naming, diff review, grouping). We can be
faster than Electron tools and lighter than container-first ones.

## Current state (codebase map)
- **cwd is already plumbed** wire→provider: `protocol.go:66-70` → `agent.go:43` → `hub.go:216`. It reaches
  `cmd.Dir` for **claude-code** (`claudecode.go:65-73`) and **pi** (`pi.go:52-60`).
- **opencode silently drops cwd** — `Create` POSTs an empty body, ignores its `cwd` arg (`opencode.go:60-65`).
  Fix: opencode honors a per-request `?directory=<abs>` query param (or `x-opencode-directory` header) on
  `POST /session` and `POST /session/{id}/message` — verified in its SDK types. One `opencode serve` drives
  many directories.
- **No project/folder concept** in the hub — sessions keyed by ID only (`hub.go:24`); `protocol.Session` is
  just `{ID, Provider, Status, Title}` (`protocol.go:132-137`). No cwd/project surfaced to the app.
- **Connection model is strictly single-daemon** — Swift `Model` holds one `{wsURL, daemonPubHex, secret}`
  (`OculusUI.swift:14-54`); daemon writes one `~/.oculus/pairing.json` (`main.go:160-176`). A daemon is
  identified **only by pubkey — no name/label anywhere**. `discover.list` finds agent artifacts within one
  host, not daemons.
- **No git/worktree code** exists. Natural insertion point: the `TypeSessionCreate` case
  (`hub.go:203-224`), just before `p.Create(ctx, req.Cwd, req.Prompt)` — resolve a worktree path, pass it as cwd.

## Harness recipes (reuse, no new runtime)
- **opencode**: `?directory=<abs-worktree>` on `/session` + `/session/{id}/message`.
- **claude-code**: set `options.cwd = <worktree>` in `sidecar.mjs` (or the sidecar's `cmd.Dir`). cwd controls
  file tools, bash, and CLAUDE.md/skill discovery.
- **pi**: no cwd flag — spawn `pi --mode rpc` with `cmd.Dir = <worktree>`; add `-a`/`--approve` so it trusts
  the worktree's project files (non-interactive modes otherwise ignore project-local config).
- **git**: repo root via `git rev-parse --show-toplevel`; `git worktree add <path> -b <branch>`;
  `git worktree remove <path>` (needs `-f` if dirty); `git worktree prune`. Worktrees share the object store,
  so commits are immediately visible to the main repo. A branch checks out in only one worktree.

---

## Track 1 — Projects & folders
**Goal:** register folders as projects; spawn sessions scoped to one; group sessions by project.

- **1.1 Fix opencode cwd** (`opencode.go`): thread `directory` into the create + message query string. Small,
  unblocks everything. *(Do first.)*
- **1.2 Project model + registry:**
  - `protocol`: `Project {ID, Name, Path, IsGitRepo, DefaultBranch}`; messages `project.list` / `project.add {path}`
    / `project.remove {id}`. Daemon persists `~/.oculus/projects.json`.
  - `SessionCreate` gains `ProjectID` (and Track-2 fields). Hub resolves cwd from the project.
  - `protocol.Session` gains `ProjectID` / `Cwd` / `WorkspaceName` / `Branch` so the app groups.
  - **Folder registration is Mac-side** (the folder lives on the Mac): the Mac app adds projects via a native
    folder picker → `project.add`. iOS just lists existing projects and spawns into them. (Optional later:
    a daemon `fs.browse` so iOS can pick a Mac folder remotely.)
- **1.3 App:** Projects list; "New session in <project>"; sidebar sessions grouped by project.

## Track 2 — Worktree automation
**Goal:** one isolated branch per session, bootstrapped, with a clean finish flow.

- **2.1 `daemon/worktree` package:** detect repo root; `Create(repo, name) → (path, branch)` doing
  `git worktree add ~/.oculus/worktrees/<repo>/<name> -b oculus/<name>`; `Remove(path, force)`; `Prune`.
  `session.create {worktree:true, workspace_name}` → create worktree, pass path as cwd (flows through to all
  3 providers; pi gets `-a`). Worktrees live **outside the repo** (`~/.oculus/worktrees/...`), Conductor-style,
  to avoid nested-git and pollution.
- **2.2 Setup hooks — `.oculus/project.json`** in the repo:
  `{ "setup": "pnpm install", "copy": [".env", ".env.local"], "portRange": [4000, 4099], "skipHooks": true }`.
  After `worktree add`: copy the listed gitignored files, run `setup`, allocate a port from the range and
  inject it (env `OCULUS_PORT` / `$PORT`). Directly kills pitfalls #1/#2/#4. (Format is ours; we can also read
  `.cursor/worktrees.json` setup hooks for drop-in compat.)
- **2.3 Finish flow:** expose `git diff` (base…worktree) to the app for review; then **checkpoint-rebase**
  (commit + merge/rebase into base) as the default local finish (Uzi-style), with `git worktree remove` +
  `prune` cleanup. Optional "create PR" via `gh` when a GitHub remote exists.
- **2.4 Polish:** auto-clean policy (idle N hours / max count, Cursor-style); "shared files" overlap warning
  across active worktrees (pitfall #5); per-session skip-hooks toggle (pitfall #3).

## Track 3 — Multi-desktop connections
**Goal:** the iOS app pairs with several Macs, names them, groups sessions by machine.

- **3.1 Daemon identity:** `--name` flag (default = hostname); include `name` in `pairing.json`, the QR payload,
  and `device.register`. Daemon keyed by its stable pubkey + human name.
- **3.2 App multi-desktop list:** replace the single `{wsURL, pub, secret}` with a persisted **array of
  `Desktop {id=pubkey, name, wsURL, secret}`** (Keychain). Desktop switcher; sidebar groups sessions under
  their desktop. First cut: one active connection, fast switch.
- **3.3 Simultaneous connections:** hold N `OculusClient`s live at once; a unified sidebar grouped by desktop
  so it feels seamless. (This is the "seamless" end-state; 3.2 ships value first.)

## Decisions (locked 2026-07-16)
- **Worktrees are opt-in per session** — default off; a toggle on "New session" creates the isolated
  worktree+branch. Non-git folders / quick edits run in-place. (Remember the last choice per project.)
- **Finish flow = create PR + remove worktree on explicit request** (not automatic). **Lean on the harness:**
  the coding agent itself has bash/git, so commit + `gh pr create` can be driven *through the agent*, with
  daemon-level git as the reliable fallback. The daemon owns the `worktree remove`/`prune` cleanup action.
- **Multi-desktop = simultaneous connections now** — hold N desktops live at once, unified sidebar grouped by
  machine.
- **Execution = one large TDD-driven plan, done in order** (Track 1 → 2 → 3), tests first, commit per phase.
  Container-based hard isolation (Sculptor-style) stays deferred.

## TDD execution checklist
Each box = tests first (red) → implement (green) → commit. Go tests in `daemon/...`; Swift where noted.

**Track 1 — Projects & folders**
- [x] 1.1 opencode honors cwd: test `Create`/`Prompt` send `?directory=<cwd>`; implement in `opencode.go`.
- [x] 1.2 Project registry: `protocol.Project` + `project.list/add/remove`; daemon `~/.oculus/projects.json`
  (test add/list/remove + persistence + reject non-existent path). `SessionCreate.ProjectID` → hub resolves cwd.
- [x] 1.3 Session metadata: `protocol.Session` carries `ProjectID/Cwd/WorkspaceName/Branch`; surfaced in
  `session.list` + events (Go test). App groups the sidebar by project (Swift).

**Track 2 — Worktrees**
- [x] 2.1 `daemon/worktree` pkg: `RepoRoot`, `Create(repo,name)→(path,branch)`, `Remove(force)`, `Prune`
  (tests vs a temp git repo). `SessionCreate.Worktree=true` → hub creates worktree, passes path as cwd; pi `-a`.
- [x] 2.2 Setup hooks `.oculus/project.json` (`setup`, `copy[]`, `portRange`, `skipHooks`): copy gitignored
  files, run setup, allocate+inject a port (tests: copies `.env`, runs cmd, unique port per worktree).
- [x] 2.3 Finish flow: `git diff` base…worktree surfaced to the app (test); daemon `worktree.remove` action
  (test); "create PR" driven via the agent harness (gated/live), `gh` fallback.
- [x] 2.4 Polish: auto-clean policy (idle/max-count), cross-worktree shared-file warning, skip-hooks toggle.

**Track 3 — Multi-desktop (simultaneous)**
- [x] 3.1 Daemon `--name` (default hostname) in `pairing.json` + QR payload + `device.register` (Go test:
  pairing.json includes name; QR decodes name).
- [x] 3.2 App: persist a `[Desktop{id=pubkey,name,wsURL,secret}]` (Keychain); a connection manager holding
  **N live `OculusClient`s** at once (Swift).
- [x] 3.3 Unified sidebar grouped by desktop; add/name/remove desktops; per-desktop status. (Swift.)
