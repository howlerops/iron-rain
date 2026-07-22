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
  send({ t: "approval", id, tool: toolName, detail });
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
    pushInput(null);
  } else if (m.t === "model") {
    // Switch the model for subsequent turns (SDK setModel accepts an alias like "opus" or a full id).
    if (currentQuery && m.text) {
      try { currentQuery.setModel(String(m.text)); } catch {}
    }
  }
});

// --- run the session ---
const planMode = process.env.OCULUS_PLAN === "1";
const options = {
  permissionMode: planMode ? "plan" : "default",
  includePartialMessages: true,
  canUseTool,
};
if (process.env.OCULUS_MODEL) options.model = process.env.OCULUS_MODEL; // create-time model
if (mode === "attach" && sessionLabel) {
  // Take-over = resume the session's full history but FORK it into a fresh id. Claude Code
  // has no live multi-client attach for plain sessions, and two writers on one session id
  // interleave/corrupt the transcript (docs: sessions.md). Forking gives the app a clean,
  // owned continuation and leaves any copy still live in a terminal untouched.
  // Resume claude's REAL session UUID (passed by the daemon from its resume map); fall back to the
  // label only if unknown. Passing our cc_… id errors: "--resume requires a valid session ID".
  options.resume = process.env.OCULUS_CLAUDE_RESUME || sessionLabel;
  options.forkSession = true;
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
}
