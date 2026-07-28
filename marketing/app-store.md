# Iron Rain — App Store / TestFlight Listing Copy

Draft for App Store Connect. Paste each field into the matching slot. Character
counts are noted where Apple enforces a limit; the limit is the hard cap, not a
target. There is no `fastlane/metadata` directory wired for this, so treat this
file as the source of truth to copy from.

Product: **Iron Rain** — a native Apple (macOS + iOS) Agent Development
Environment. Repo: github.com/howlerops/iron-rain. Home:
https://howlerops.github.io/iron-rain/

---

## 1. App Name (30 char max)

```
Iron Rain: Coding Agents
```

*(24 chars)*

Alternate, if a category word reads better in your slot:

```
Iron Rain: Code Agent ADE
```

*(25 chars)*

---

## 2. Subtitle (30 char max)

```
Run & approve agents remotely
```

*(29 chars)*

Alternates:

```
Your agents. Your Mac. Anywhere.
```

*(too long — 32; trim to)* `Your agents, your Mac, anywhere` *(31 — still over; use below)*

```
Steer coding agents anywhere
```

*(28 chars)*

---

## 3. Promotional Text (170 char max)

```
Launch real coding agents on your Mac, then watch, steer, and approve them from your iPhone — from anywhere. End-to-end encrypted. Your hardware, your keys.
```

*(154 chars)*

---

## 4. Description

```
Iron Rain runs real coding agents on your own Mac and lets you drive them from your Mac or your iPhone, from anywhere. Kick off a task at your desk, close the laptop, and keep shipping from your phone. When an agent hits a command that needs sign-off, you approve or deny it right from the lock screen.

It's a native Apple app, not a web wrapper. The agents run on your hardware, using your own subscriptions and keys. Traffic between your devices is end-to-end encrypted (X25519 / ChaCha20-Poly1305); the relay only ever forwards ciphertext, so it can't read your code or your prompts. No analytics or tracking by default.

WORKS WITH THE AGENTS YOU ALREADY USE
- opencode, Claude Code, and pi out of the box, plus any custom CLI agent (codex, gemini, aider, and friends).
- Pick or switch the model per session. Providers are auto-detected.

APPROVE FROM ANYWHERE
- A tool call that needs sign-off pushes to your iPhone. Tap the notification, read the command, approve or deny.
- Set cost budgets and spending guardrails so a run can't get away from you.

A REAL CHAT SURFACE
- Native composer with slash-command completion, per-session drafts, and no smart-quotes mangling your code.
- Streaming markdown and thinking, rich inline tool cards with expandable output, and collapsible sub-agents that stream their own work.
- Generative UI: agents render native tables, checklists, callouts, diffs, and tappable choices inline — even on a plain CLI agent.

TICKETS AND CODE, NOT JUST CHAT
- Two-way Jira and Linear editing: assignee, labels, sprint or cycle, estimate, due date, comments. A real-status Kanban board with drag-drop transitions, plus issue-to-PR loops.
- Native syntax-highlighted editor, file tree, and LSP (diagnostics, hover, go-to-definition, completion, rename).
- Review a diff and comment on it to steer the agent, scoped to the session.

ORCHESTRATION FOR REAL WORK
- Fan out N agents on the same prompt in isolated worktrees, then compare and merge the winner.
- Delegate scoped sub-agents, and let an autonomous heartbeat nudge a session to completion inside a cost budget.
- Checkpoint a worktree to snapshot or roll back, and set up Loops for recurring ticket workflows.

BUILT FOR MANY REPOS AND MANY MACHINES
- Cross-repo workspaces and git worktrees that share node_modules instead of reinstalling.
- SSH remotes to run and inspect a worktree on another box, with port-forward.
- Design Mode: pick an element in the in-app browser and drop its HTML/CSS straight into your prompt.

COMMAND DECK
- Five destinations — Sessions, Loops, Fleet, Issues, Activity — with a fleet dashboard, a "needs you" inbox, and a Cmd-K palette.

STAYS UP WHEN THINGS GO SIDEWAYS
- Write-ahead transcript and a no-response watchdog that self-heals a session when the agent stream drops mid-turn.
- One-tap session recovery, cost and token meters, provider quota probing, and multi-account credential hot-swap.

Install the Mac daemon in one line from howlerops.github.io/iron-rain and get the iOS app via TestFlight. Your agents, your Mac, anywhere.
```

---

## 5. Keywords (100 char max, comma-separated)

```
claude code,opencode,codex,gemini,AI coding,code review,pair programming,SSH,LSP,kanban,jira,linear
```

*(99 chars)*

Notes: "agent", "remote", and "approve" are intentionally omitted here because
they already appear in the app name / subtitle, and the App Store indexes those
automatically. Don't repeat them; spend the budget on terms that aren't already
covered.

---

## 6. "What's New" Template

Fill in the version and 3–5 concrete lines. Lead with what changed for the user,
not internal refactors. Keep the voice plain.

```
Iron Rain <VERSION>

- <Headline change — the thing most people will notice first.>
- <Second concrete improvement, with the surface it touches (Chat, Issues, Fleet, ...).>
- <A fix that people actually hit — name the symptom, not the commit.>
- <Smaller addition or polish.>

Full notes: github.com/howlerops/iron-rain/releases
```

Worked example (for reference — replace before shipping):

```
Iron Rain 0.2.93

- Sub-agents now stream their own work inline and collapse when they're done, so a big delegation doesn't bury the main thread.
- Worktrees share node_modules instead of reinstalling, so fanning out N agents is fast and doesn't fill your disk.
- The no-response watchdog now self-heals: if an agent's stream drops mid-turn, the session recovers instead of hanging.
- Kanban board gained drag-drop transitions and inline ticket creation for Jira and Linear.

Full notes: github.com/howlerops/iron-rain/releases
```

---

## 7. GitHub Repo "About" (one line)

Set on github.com/howlerops/iron-rain:

```
Native macOS + iOS Agent Development Environment — run real coding agents on your Mac, steer and approve them from anywhere, end-to-end encrypted.
```

Website field: `https://howlerops.github.io/iron-rain/`
Suggested topics: `coding-agents`, `claude-code`, `opencode`, `swiftui`, `macos`, `ios`, `end-to-end-encryption`, `developer-tools`
