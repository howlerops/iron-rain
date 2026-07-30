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
//   sidecar -> daemon : {"t":"session","id"} | {"t":"text","text"} | {"t":"thinking","text"}
//                       {"t":"tool","tool","detail"} | {"t":"approval","id","tool","detail"}
//                       {"t":"idle"} | {"t":"error","message"}
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

// --- daemon -> sidecar commands ---
createInterface({ input: process.stdin }).on("line", (line) => {
  line = line.trim();
  if (!line) return;
  let m;
  try {
    m = JSON.parse(line);
  } catch {
    return;
  }
  if (m.t === "prompt") {
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
    permissionMode = m.text === "architect" || m.text === "ask" ? "plan" : "default";
    if (currentQuery && typeof currentQuery.setPermissionMode === "function") {
      try { currentQuery.setPermissionMode(permissionMode); } catch {}
    }
  } else if (m.t === "model") {
    // Switch the model for subsequent turns (SDK setModel accepts an alias like "opus" or a full id).
    if (currentQuery && m.text) {
      try { currentQuery.setModel(String(m.text)); } catch {}
    }
  }
});

// --- run the session ---
const planMode = process.env.OCULUS_PLAN === "1";
let permissionMode = planMode ? "plan" : "default";
const options = {
  permissionMode,
  includePartialMessages: true,
  canUseTool,
};
if (process.env.OCULUS_MODEL) options.model = process.env.OCULUS_MODEL; // create-time model
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
        if (message.subtype === "init" && message.session_id) {
          send({ t: "session", id: message.session_id });
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
        send({ t: "idle" });
        break;
      }
    }
  }
} catch (e) {
  send({ t: "error", message: String((e && e.message) || e) });
  // The query loop has thrown and is DEAD. The stdin readline would keep this process alive as a
  // zombie that silently drops every future prompt (the "claude-code stopped responding to new
  // messages" wedge). Emit idle so nothing is stuck "working", then exit — the daemon sees stdout
  // EOF, reaps us, and surfaces the session as stopped/restartable so the user can Restart cleanly.
  send({ t: "idle" });
  process.exit(1);
}
