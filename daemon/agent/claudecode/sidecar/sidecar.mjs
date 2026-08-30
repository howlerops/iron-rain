#!/usr/bin/env node
// Oculus claude-code sidecar.
//
// Drives a PERSISTENT streaming claude-code session via the Claude Agent SDK and
// bridges it to the Go daemon over stdio (line-delimited JSON). This replaces the
// old single-shot `claude -p` design whose PreToolUse hook does NOT block in -p mode
// (anthropics/claude-code#36071). Here the SDK's canUseTool callback genuinely blocks
// the tool until the daemon answers, so approvals are enforced.
//
// Protocol (see ../claudecode.go):
//   daemon -> sidecar : {"t":"prompt","text"} | {"t":"approval","id","decision"} | {"t":"stop"}
//                       {"t":"ping","id"}
//   sidecar -> daemon : {"t":"session","id"} | {"t":"text","text"} | {"t":"thinking","text"}
//                       {"t":"tool","tool","detail"} | {"t":"approval","id","tool","detail"}
//                       {"t":"idle"} | {"t":"error","message"} | {"t":"pong","id","busy"}
//
// Auth: uses your logged-in `claude` CLI, i.e. your claude.ai SUBSCRIPTION — no API
// key needed (verified live). Set ANTHROPIC_API_KEY only to use a metered key instead.
// Run:  node sidecar.mjs   (with OCULUS_SESSION_ID / OCULUS_MODE set by the daemon).
import { query } from "@anthropic-ai/claude-agent-sdk";
import { createInterface } from "node:readline";

const sessionLabel = process.env.OCULUS_SESSION_ID || "";
const mode = process.env.OCULUS_MODE || "create";

function send(obj) {
  process.stdout.write(JSON.stringify(obj) + "\n");
}

// toolSummary renders a short human command line from a tool's input (Bash → command, file tools →
// path, search → pattern), so a tool card reads "Bash · npm test" instead of just the tool name.
function toolSummary(name, input) {
  if (!input || typeof input !== "object") return "";
  const i = input;
  const pick = i.command || i.file_path || i.path || i.pattern || i.url || i.query || i.prompt;
  if (typeof pick === "string") return pick.length > 200 ? pick.slice(0, 200) + "…" : pick;
  try { const s = JSON.stringify(i); return s.length > 160 ? s.slice(0, 160) + "…" : s; } catch { return ""; }
}

// toolResultText flattens a tool_result's content (string or array of text blocks) to plain text,
// capped so a huge result can't flood the wire.
function toolResultText(content) {
  let text = "";
  if (typeof content === "string") text = content;
  else if (Array.isArray(content)) text = content.map((b) => (typeof b === "string" ? b : b?.text || "")).join("");
  return text.length > 8000 ? text.slice(0, 8000) + "\n…(truncated)" : text;
}

// --- streaming user-input queue: the daemon pushes prompts over time ---
let waiting = null;
const pending = [];
let ended = false;
function pushInput(text) {
  if (waiting) {
    const w = waiting;
    waiting = null;
    w(text);
  } else {
    pending.push(text);
  }
}
function nextInput() {
  return new Promise((resolve) => {
    if (pending.length) resolve(pending.shift());
    else waiting = resolve;
  });
}
async function* inputGen() {
  while (!ended) {
    const text = await nextInput();
    if (text === null) return;
    yield { type: "user", message: { role: "user", content: text }, parent_tool_use_id: null };
  }
}

// --- approvals: canUseTool blocks until the daemon answers ---
let approvalSeq = 0;
const approvals = new Map();
function canUseTool(toolName, input) {
  const id = "ap_" + ++approvalSeq;
  // Plan mode: ExitPlanMode's input.plan is the proposed plan — surface it so the human can
  // approve the plan itself (on their phone) before any changes are made.
  const detail =
    (toolName === "ExitPlanMode" && input?.plan) ||
    input?.command || input?.file_path || input?.path || input?.plan ||
    (input ? JSON.stringify(input).slice(0, 160) : "");
  // Forward the tool's FULL arguments, not just the truncated display string. The daemon's rule
  // engine scopes an "always allow" by argument (a path subtree, a command shape), which it can't do
  // from a 160-char summary. The daemon caps/validates before anything is persisted or rendered.
  send({ t: "approval", id, tool: toolName, detail, input: input ?? null });
  return new Promise((resolve) => approvals.set(id, { resolve, input }));
}

// The active query handle (set once the session loop starts), so a mid-session model switch can
// call query.setModel(). Null until the loop begins.
let currentQuery = null;

// busy is this process's own answer to "is a turn in flight?", for the daemon's liveness probe.
// It is authoritative in a way the event stream is not: events can be lost or simply never come
// (a wedged tool emits nothing at all), and the daemon must still be able to tell a working agent
// from a dead one without guessing from timers.
let busy = false;

// --- daemon -> sidecar commands ---
const rl = createInterface({ input: process.stdin });
rl.on("line", (line) => {
  line = line.trim();
  if (!line) return;
  let m;
  try {
    m = JSON.parse(line);
  } catch {
    return;
  }
  if (m.t === "ping") {
    // Liveness probe. Answered synchronously from the readline handler — deliberately NOT from
    // inside the query loop, so a turn wedged in a tool still replies and the daemon learns
    // "alive and busy" rather than "unreachable".
    send({ t: "pong", id: m.id ?? "", busy });
    return;
  }
  if (m.t === "prompt") {
    busy = true;
    if (Array.isArray(m.images) && m.images.length) {
      // Multimodal turn: a content array of a text block + Anthropic image blocks.
      const content = [];
      if (m.text) content.push({ type: "text", text: String(m.text) });
      for (const im of m.images) {
        content.push({ type: "image", source: { type: "base64", media_type: im.mime, data: im.data } });
      }
      pushInput(content);
    } else {
      pushInput(String(m.text ?? ""));
    }
  } else if (m.t === "approval") {
    const a = approvals.get(m.id);
    if (a) {
      approvals.delete(m.id);
      a.resolve(
        m.decision === "allow"
          ? { behavior: "allow", updatedInput: a.input }
          : { behavior: "deny", message: "Denied by user" }
      );
    }
  } else if (m.t === "stop") {
    ended = true;
    pushInput(null); // end the input generator so the loop finishes after the current turn
    // ...and INTERRUPT the in-flight turn so Stop actually aborts work-in-progress, not just prevents
    // the next turn (without this, "stop" did nothing until the running turn finished on its own).
    if (currentQuery && typeof currentQuery.interrupt === "function") {
      try { currentQuery.interrupt(); } catch {}
    }
  } else if (m.t === "mode") {
    // Switch the permission mode for subsequent turns. The daemon enforces its own rules regardless;
    // this makes the MODEL aware it should be planning rather than editing, which changes what it
    // proposes, not just what it's allowed to do.
    // yolo maps onto the SDK's bypassPermissions, which has a consequence the daemon relies on
    // knowing: in that mode the SDK stops calling canUseTool, so the approval callback below is
    // never consulted again. The daemon auto-allows in yolo anyway, so the two agree — but they
    // agree by construction, not by accident, and changing either half alone would break it.
    permissionMode =
      m.text === "yolo" ? "bypassPermissions"
      : m.text === "architect" || m.text === "ask" ? "plan"
      : "default";
    if (currentQuery && typeof currentQuery.setPermissionMode === "function") {
      try { currentQuery.setPermissionMode(permissionMode); } catch {}
    }
    // Say so. A mode change the user cannot see is the same class of problem as not knowing the
    // mode at all — it decides what happens without asking.
    send({ t: "facts", mode: permissionMode });
  } else if (m.t === "model") {
    // Switch the model for subsequent turns (SDK setModel accepts an alias like "opus" or a full id).
    if (currentQuery && m.text) {
      try { currentQuery.setModel(String(m.text)); } catch {}
    }
  }
});

// --- stdin EOF: the daemon is gone, so we must go too ---
//
// This is the ONE teardown path that survives every way the daemon can die, including a SIGKILL, an
// OOM kill or a panic that runs no cleanup at all: however the parent went, the kernel closes its end
// of our stdin pipe and we read EOF. It has to exist because the daemon deliberately starts us in our
// OWN process group (daemon/procutil.Isolate, so a runaway tool tree can be killed as a unit) — which
// also means we never receive the process-group signals that would otherwise take us down with the
// daemon. NOTHING kills this process implicitly. Before this handler existed, the readline above kept
// the event loop alive forever with no reader on the other end, and every abandoned sidecar sat here
// holding an open Agent SDK connection and a live `claude` child of its own. Measured on one machine:
// 143 orphaned sidecars, 284 processes counting their children, 12.7 GB resident, the oldest a week
// old — from ordinary daemon restarts.
//
// EOF on a pipe is not a transient condition and cannot be retried: it means the write end is closed,
// i.e. the daemon called Close() on us or the daemon is dead. Either way this session is over.
const exitGraceMs = Number(process.env.OCULUS_SIDECAR_EXIT_GRACE_MS || 5000);
let shuttingDown = false;

function shutdownOnEOF() {
  if (shuttingDown) return;
  shuttingDown = true;
  ended = true;
  pushInput(null); // end the input generator so the query loop finishes instead of awaiting forever
  // Interrupt the in-flight turn so the SDK tears down its `claude` child through its OWN cleanup —
  // that is what lets the loop below fall through to a normal exit with nothing orphaned one level
  // down. This does not truncate the stream: writes already handed to stdout stay queued, and the
  // SDK's post-interrupt `result` message is still forwarded by the loop that is running right now,
  // so a graceful daemon-side Close() gets its final usage/idle frames rather than a cut-off stream.
  try {
    if (currentQuery && typeof currentQuery.interrupt === "function") currentQuery.interrupt();
  } catch {}
  // Backstop, deliberately unref'd: if the query loop does wind down, this timer never holds the
  // process open and we exit naturally with the SDK's cleanup done. It fires ONLY when something
  // else is still keeping the event loop alive (a child holding a pipe, an await that never settles)
  // — which is exactly the state that produced the week-old orphans.
  const t = setTimeout(() => hardExit(0), exitGraceMs);
  if (typeof t.unref === "function") t.unref();
}

// hardExit is the last resort, used only when the graceful wind-down above did not finish in time.
// A plain process.exit() would leave the SDK's `claude` child running and simply move the leak one
// level down, so we signal our OWN process group first: a negative pid names a whole process group,
// and when we lead our own group that is exactly this sidecar plus everything it spawned.
//
// It is only ever safe to do that if we ARE the group leader — otherwise the negative pid names
// somebody else's group, which is unthinkable. Node exposes no getpgrp(), so we do not guess: the
// daemon sets OCULUS_SIDECAR_PGLEADER when (and only when) it called procutil.Isolate on us, which is
// what makes pgid == pid true. Started any other way the flag is absent and we exit alone, accepting
// a possible orphan rather than risking a signal aimed at processes we do not own.
function hardExit(code) {
  try {
    if (process.env.OCULUS_SIDECAR_PGLEADER === "1") process.kill(-process.pid, "SIGKILL");
  } catch {}
  process.exit(code);
}

rl.on("close", shutdownOnEOF);

// --- sub-agent tracking ---
//
// The SDK does not announce a sub-agent starting; it just starts tagging messages with the Task
// tool's id. So "have I seen this parent before" IS the start signal. Bounded because a turn runs a
// handful of sub-agents, not thousands, and the set dies with the process.
const seenSubAgents = new Set();
function noteSubAgent(parentToolUseID) {
  if (!parentToolUseID || seenSubAgents.has(parentToolUseID)) return;
  seenSubAgents.add(parentToolUseID);
  send({ t: "subagent", id: String(parentToolUseID), status: "running" });
}

// --- run the session ---
const planMode = process.env.OCULUS_PLAN === "1";
let permissionMode = planMode ? "plan" : "default";
const options = {
  permissionMode,
  includePartialMessages: true,
  canUseTool,
  // Without this the SDK runs a BARE model: no Claude Code system prompt, so no tool-use discipline,
  // no file-editing conventions, none of the behaviour the product name promises. Sessions we start
  // were literally not Claude Code.
  systemPrompt: { type: "preset", preset: "claude_code" },
};
if (process.env.OCULUS_MODEL) options.model = process.env.OCULUS_MODEL; // create-time model
// MCP servers registered once with the daemon and injected here, so the user configures a server in
// ONE place instead of separately per harness. Malformed JSON is ignored rather than fatal: losing
// MCP tools is far better than failing to start the session at all.
if (process.env.OCULUS_MCP_CONFIG) {
  try {
    const servers = JSON.parse(process.env.OCULUS_MCP_CONFIG);
    if (servers && typeof servers === "object" && Object.keys(servers).length) {
      options.mcpServers = servers;
      // EXCLUSIVE mode: ignore .mcp.json, user settings and plugins, so a server the daemon manages
      // isn't ALSO started by the harness from its own config. Without this the SDK loads both sets:
      // identical names collide unpredictably, and the same server under two names runs twice — two
      // processes and two sets of credentials, which is exactly what the daemon's gateway exists to
      // prevent. Opt-in, because turning it on when the user has servers we didn't import would
      // silently remove tools they rely on.
      if (process.env.OCULUS_MCP_EXCLUSIVE === "1") options.strictMcpConfig = true;
    }
  } catch (e) {
    send({ t: "error", message: "ignoring malformed MCP config: " + (e?.message || e) });
  }
}
if (mode === "attach" && sessionLabel) {
  // Take-over = resume the session's full history but FORK it into a fresh id. Claude Code
  // has no live multi-client attach for plain sessions, and two writers on one session id
  // interleave/corrupt the transcript (docs: sessions.md). Forking gives the app a clean,
  // owned continuation and leaves any copy still live in a terminal untouched.
  // Resume ONLY with claude's REAL session UUID (from the daemon's resume map). If it's unknown
  // (session created before resume-mapping, or the map was lost) we CANNOT resume claude's
  // history — so start a FRESH session instead of passing our cc_… id to --resume, which claude
  // rejects ("--resume requires a valid session ID") and which used to kill the session on every
  // restore, leaving it permanently broken.
  const realResume = process.env.OCULUS_CLAUDE_RESUME;
  if (realResume) {
    options.resume = realResume;
    options.forkSession = true;
  }
}

send({ t: "session", id: sessionLabel });

try {
  currentQuery = query({ prompt: inputGen(), options });
  for await (const message of currentQuery) {
    switch (message.type) {
      case "system":
        if (message.subtype === "init") {
          if (message.session_id) send({ t: "session", id: message.session_id });
          // The init message is the richest thing the SDK ever sends and we were taking one field
          // out of it. Model, permission mode, cwd and the slash-command list were all right here
          // and discarded, which is why the app could not tell you which mode you were in while
          // claude's own TUI showed it in the status bar.
          send({
            t: "facts",
            model: String(message.model || ""),
            mode: String(message.permissionMode || permissionMode || ""),
            cwd: String(message.cwd || ""),
            commands: Array.isArray(message.slash_commands) ? message.slash_commands : [],
            mcp: Array.isArray(message.mcp_servers) ? message.mcp_servers.length : 0,
          });
        } else if (message.subtype === "compact_boundary") {
          // History was just compacted, so any context figure the client is showing is now stale.
          // Reporting it as an event is the difference between a context meter that resets and one
          // that silently lies until the next turn.
          send({ t: "compacted" });
        }
        break;
      case "stream_event": {
        const ev = message.event;
        if (ev?.type === "content_block_delta") {
          if (ev.delta?.type === "text_delta") send({ t: "text", text: ev.delta.text });
          else if (ev.delta?.type === "thinking_delta") send({ t: "thinking", text: ev.delta.thinking });
        } else if (ev?.type === "content_block_start" && ev.content_block?.type === "tool_use") {
          send({ t: "tool", tool: ev.content_block.name, detail: "" });
        }
        break;
      }
      case "assistant": {
        // parent_tool_use_id is set on everything a SUB-AGENT produces (the Task tool's own
        // conversation). opencode already reported sub-agents and claude-code did not, so the
        // richer provider looked like it had none — work would happen with nothing on screen to say
        // who was doing it. Announce each parent once; the adapter turns this into the same
        // session.subagent event opencode emits.
        noteSubAgent(message.parent_tool_use_id);
        const blocks = message.message?.content;
        if (Array.isArray(blocks)) {
          let latestTodoBlock = null;
          for (const block of blocks) {
            if (block && typeof block === "object" && block.name === "TodoWrite") {
              latestTodoBlock = block;
            }
            // Rich tool card: emit each tool_use with a human command summary (running). The
            // matching output arrives later as a tool_result in a "user" message (see below).
            if (block?.type === "tool_use" && block.id) {
              send({ t: "toolcall", id: block.id, tool: String(block.name || "tool"),
                     detail: toolSummary(block.name, block.input), status: "running" });
            }
          }
          if (latestTodoBlock) {
            const todos = Array.isArray(latestTodoBlock.input?.todos) ? latestTodoBlock.input.todos : [];
            send({
              t: "todos",
              todos: todos.map((td) => ({
                content: String(td?.content ?? td?.activeForm ?? ""),
                status: String(td?.status ?? "pending"),
              })),
            });
          }
        }
        break;
      }
      case "user": {
        // Tool results arrive as tool_result blocks in a user message — pair them to their tool_use
        // by id and fill in the card's output.
        const blocks = message.message?.content;
        if (Array.isArray(blocks)) {
          for (const block of blocks) {
            if (block?.type === "tool_result" && block.tool_use_id) {
              send({ t: "toolcall", id: block.tool_use_id, output: toolResultText(block.content),
                     status: block.is_error ? "error" : "completed" });
            }
          }
        }
        break;
      }
      case "result": {
        const inputTokens = Number(message.usage?.input_tokens ?? 0);
        const outputTokens = Number(message.usage?.output_tokens ?? 0);
        const costUsd = Number(message.total_cost_usd ?? 0);
        if (inputTokens > 0 || outputTokens > 0 || costUsd > 0) {
          send({ t: "usage", input_tokens: inputTokens, output_tokens: outputTokens, cost_usd: costUsd });
        }
        busy = false;
        send({ t: "idle" });
        break;
      }
    }
  }
} catch (e) {
  busy = false;
  send({ t: "error", message: String((e && e.message) || e) });
  // The query loop has thrown and is DEAD. The stdin readline would keep this process alive as a
  // zombie that silently drops every future prompt (the "claude-code stopped responding to new
  // messages" wedge). Emit idle so nothing is stuck "working", then exit — the daemon sees stdout
  // EOF, reaps us, and surfaces the session as stopped/restartable so the user can Restart cleanly.
  send({ t: "idle" });
  process.exit(1);
}

// The query loop ended normally. If stdin already EOF'd there is no daemon left to talk to and no
// further prompt can ever arrive, so leave — the loop having completed means the SDK's own cleanup
// already ran and its `claude` child is gone, which is why this is a plain exit and not hardExit.
// Without this the readline above would keep the process alive at EOF with nothing to read.
if (shuttingDown) process.exit(0);
