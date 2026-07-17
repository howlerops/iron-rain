# Plan: auto-projects + a Linear/Jira ticket system

Two features: (1) **auto-create projects** from the folders agents are already running in;
(2) a per-Mac **ticket-management integration** (Linear first, Jira second) with a Linear-like
kanban/table UI that launches agents from tickets, bidirectionally.

---

## Feature 1 — Auto-projects from active agents

**Goal:** any folder you're already running an agent in shows up as a project, zero clicks.

**Ground truth (codebase map):**
- `project.Registry.Add(dir)` dedupes by abs path (`project.go:73`); `worktree.RepoRoot(dir)`
  (`worktree.go:23`) resolves the git root. Nothing calls Add from discovery today.
- **The catch — cwd coverage is uneven:**
  - **daemon-managed sessions**: the hub already knows the cwd (`sessionMeta.cwd`) — trivially auto-registerable.
  - **claude-code (discovered)**: `Cwd` is derived *lossily* from `~/.claude/projects/<encoded>` dir
    names (`decodeProjectDir`, `discovery.go:154`) — breaks on paths containing `-`.
  - **opencode (discovered)**: servers/sessions carry **no cwd** (only URL/PID/Title).
  - **pi**: not discovered at all.

**Plan:**
- **1.1 Register cwds we already know** (easy win): after each `discover.list` (and on session
  create), for every session with a known cwd, `reg.Add(RepoRoot(cwd))` (deduped). Covers all
  daemon-managed sessions + claude-code. Gate behind `--auto-projects` (default on).
- **1.2 Close the opencode/claude cwd gaps:**
  - claude-code: read the *real* cwd from the transcript (the `cwd` recorded in the first JSONL
    message) instead of the lossy dir-name decode.
  - opencode discovered servers: resolve the server process cwd via `lsof -a -p <PID> -d cwd`
    (macOS), or check whether opencode's session API exposes a path (the single highest-leverage
    unknown — verify before building).
- **1.3 Surface**: auto-registered projects flow through the existing `project.list`; the app's
  New Session picker + sidebar grouping already consume them. Mark them `source:"auto"` so the UI
  can distinguish (and offer "keep"/"forget").

Small, mostly daemon-side, TDD-able (registry + a discovery→registry pass).

---

## Feature 2 — Linear/Jira ticket system

**Architecture decision (from the map): daemon-side.** Secrets already live on the daemon
(`~/.oculus/`, 0600); the app has no HTTP client (only `OculusClient` over `/ws`); it's per-Mac,
matching the ask. The daemon holds the token, calls Linear/Jira, and serves issues to every paired
device over the existing encrypted protocol. The app just adds protocol messages + a view.

### Provider-agnostic Issue layer (`daemon/issues`)
```go
type Issue struct {
    ID        string   // Linear UUID / Jira key "ENG-123"
    Key       string   // human id: Linear identifier "ENG-42" / Jira key
    Title     string
    Body      string
    Status    string   // provider status name
    Category  string   // normalized: todo | in_progress | done | other
    Assignee  string
    URL       string
    Provider  string   // "linear" | "jira"
    BranchName string  // Linear native issue.branchName (→ worktree branch); Jira synthesized
    TeamID, ProjectID, Priority string
    UpdatedAt string
}
type Provider interface {
    Name() string
    ListAssigned(ctx) ([]Issue, error)
    WorkflowStates(ctx, teamID string) ([]State, error) // kanban columns
    Comment(ctx, issueID, body string) error
    Transition(ctx, issueID, toStateID string) error
}
```
Linear + Jira each implement `Provider`. The hub holds a registry of connected providers.

### Linear specifics (verified)
- **Endpoint:** `https://api.linear.app/graphql`.
- **Auth:** two modes — **PAT** (header `Authorization: <key>`, grants full workspace access — fastest
  to ship) OR **OAuth 2.0 + PKCE** (native-app flow, loopback redirect
  `http://127.0.0.1:6000/oauth/linear/callback` — the daemon mux can host it; no client_secret with
  PKCE; scopes `read,write,comments:create,issues:create`, plus `app:assignable,app:mentionable` for
  the **agent-actor** model where `@IronRain` becomes an assignable Linear identity). Store the token
  in `~/.oculus/integrations.json` (0600).
- **Assigned issues:** `query { viewer { assignedIssues(first:50, after:$cursor, includeArchived:false)
  { nodes { id identifier title description url branchName priority updatedAt
  state { id name type } team { id key } project { id name } } pageInfo { hasNextPage endCursor } } } }`
  — `state.type` ∈ backlog|unstarted|started|completed|canceled → our `Category`.
- **Write-back:** `commentCreate(input:{issueId, body})`; `issueUpdate(id, input:{stateId})`.
- **Sync:** **poll every 60s** (webhooks need a public HTTPS endpoint; the daemon is loopback — but we
  already have the relay/ngrok `--public-url`, so webhooks with `Linear-Signature` HMAC verification
  are a later upgrade when a public URL is configured). Rate limits are complexity-based; 60s polling
  is well within budget.

### Jira (second adapter, same interface)
- `https://api.atlassian.com/ex/jira/{cloudId}/rest/api/3/`; auth OAuth 3LO or API-token+email basic.
- Assigned: `GET /search/jql?jql=assignee=currentUser()&fields=key,summary,description,status,assignee`.
- Transition: `POST /issue/{key}/transitions`; comment: `POST /issue/{key}/comment`.
- Status→Category: To Do→todo, In Progress→in_progress, Done→done.

### Launch an agent from a ticket (the payoff)
Reuses `SessionCreate` directly: `{provider, project_id (auto-project for the issue's repo), prompt:
<issue title+body+url>, worktree:true, workspace_name: issue.branchName}`. New: a ticket↔session link
(`issueID` on `sessionMeta`/`Session`) so the app shows which ticket a session is working, and so
write-back knows where to post.

### Bidirectional
- **Inbound:** poll → diff → push updates to all devices (broadcast, like approvals).
- **Outbound:** launching from a ticket → `Transition` to "started"; on PR open → comment the PR link
  + `Transition` to "in review"; **lean on the harness** too (the agent has `gh`/git and can comment
  via a Linear MCP/CLI if configured), with daemon-level write-back as the reliable path.

### Protocol additions
`integration.connect {provider, token|oauth}` · `integration.status` · `issue.list` →
`IssueList{issues}` · `issue.launch {issue_id, provider, worktree}` → `Session` · `issue.comment
{issue_id, body}` · `issue.transition {issue_id, to_state}` · `issue.states {team_id}` →
kanban columns. (Go const+struct+dispatch + Swift const+struct+receiveLoop + golden vector, per the
established pattern.)

### App UI — Linear-like issue management
A new top-level **Issues** surface (a `TabView` or a sidebar section routing the detail to
`IssuesView`). **Kanban** (columns from the team's `WorkflowStates`, cards = assigned issues, drag to
transition) with a **table** toggle. Each card: status, priority, key, assignee, and a **"Start agent"**
action → opens `NewSessionView` pre-filled with the ticket context + `worktree=true`,
`workspaceName=branchName`. A connect screen (paste PAT or "Connect with Linear" → OAuth).

### Phases (TDD; daemon Go fully testable with a stub GraphQL/REST server)
- **2.1** `daemon/issues` interface + Linear adapter (ListAssigned/Comment/Transition/WorkflowStates)
  + token store; TDD against a stub Linear GraphQL server.
- **2.2** Protocol + hub handlers (`integration.connect`, `issue.list`, `issue.states`) + 60s poll +
  broadcast; TDD.
- **2.3** `issue.launch` → SessionCreate with branchName worktree + ticket↔session link; TDD.
- **2.4** Bidirectional write-back (comment + transition on launch/PR); TDD the daemon calls.
- **2.5** App: Issues kanban/table + connect screen + "Start agent" (swift-build-verified).
- **2.6** Jira adapter behind the same interface.
- **2.7** OAuth-PKCE flow (daemon mux `/oauth/linear/callback`) as an alternative to PAT; webhook
  ingestion when `--public-url` is set (HMAC-verified) to replace polling.

---

## MAJOR FINDING — an existing Linear-integrated service (`../linear-orchestrator`)
There's a live, deployed monorepo (`~/projects/linear-orchestrator`, internally also "oculus",
Fly app **oculus.fly.dev**, app+worker running) that ALREADY implements most of Task 2 in TS:
- `packages/linear`: OAuth (agent-actor scopes `read,write,app:assignable,app:mentionable`), the
  token exchange, **webhook HMAC verification** (`Linear-Signature`), issue sync, and Linear's
  native **AgentActivity** API (`createAgentActivity`).
- `apps/server` (Hono): `/api/auth/linear` OAuth flow, a **public HTTPS webhook receiver** (solves
  the daemon-has-no-public-URL problem), channels/runs/approvals, a normalized `@oculus/protocol`.
- The OAuth app credentials (`LINEAR_CLIENT_ID/SECRET`, `LINEAR_WEBHOOK_SECRET`, `API_TOKEN`) live in
  **Fly secrets** on the `oculus` app (retrievable via `fly ssh console -a oculus -C printenv`).

→ We reuse the **existing OAuth app** (not register a new one). The open question is how much of the
deployed service to reuse (see the fork below).

## Decisions
- ✅ **Auth:** reuse the existing Linear OAuth app (agent-actor) — values from the `oculus` Fly app.
- ✅ **Write-back:** auto (status + comment on launch/PR), with a **per-project toggle**.
- ✅ **Auto-projects (Task 1):** **full coverage now** — daemon-managed + claude-code (fix the lossy
  transcript-cwd), and discovered opencode via `lsof -a -p <PID> -d cwd` (+ verify the opencode
  session API for a path).
- ✅ **UI:** a dedicated **Issues** surface; **on mobile it must be a separate navigation destination
  from the session/chat view** (not crammed into the split-view detail) — likely a tab on macOS/iPad
  and a distinct screen on iPhone.

### OPEN FORK — how to wire Linear into Iron Rain given the deployed service
- **A. Reuse oculus.fly.dev as the Linear hub:** the daemon talks to the deployed service (`API_TOKEN`)
  for assigned issues + assignment-triggered launches; the service keeps doing OAuth + webhooks +
  sync; the daemon runs agents locally. Fastest, webhooks work today, reuses everything — but couples
  local-first Iron Rain to the cloud service.
- **B. Reimplement Linear in the Go daemon** (mirror `packages/linear`) using the same OAuth app
  creds; poll every 60s; webhooks only when `--public-url` is set. Local-first, self-contained, no
  cloud dependency — more Go code, no webhooks by default.
- **C. Hybrid:** daemon fetches issues + launches agents directly against Linear (local-first), but
  reuses oculus.fly.dev purely as the **OAuth redirect + webhook receiver** (the public URL), which
  relays assignment events to the daemon. Best of both; a little glue.
