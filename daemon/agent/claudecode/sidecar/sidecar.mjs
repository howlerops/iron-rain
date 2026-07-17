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
  const detail =
    input?.command || input?.file_path || input?.path ||
    (input ? JSON.stringify(input).slice(0, 160) : "");
  send({ t: "approval", id, tool: toolName, detail });
  return new Promise((resolve) => approvals.set(id, { resolve, input }));
}

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
  }
});

// --- run the session ---
const options = { permissionMode: "default", includePartialMessages: true, canUseTool };
if (mode === "attach" && sessionLabel) options.resume = sessionLabel;

send({ t: "session", id: sessionLabel });

try {
  for await (const message of query({ prompt: inputGen(), options })) {
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
      case "result":
        send({ t: "idle" });
        break;
    }
  }
} catch (e) {
  send({ t: "error", message: String((e && e.message) || e) });
}
