# pi.dev provider — spike

**Verdict: FEASIBLE, clean fit.** pi (`@earendil-works/pi-coding-agent`, verified vs **0.80.2**)
ships a documented **RPC mode** (`pi --mode rpc`) that maps directly onto the daemon's
single-session-broadcast model. A gated provider is implemented + unit-tested; live validation
against a real LLM turn is the remaining gate.

## Why it fits the model
pi RPC is a **JSONL protocol over one stdio pipe** to a single child process. Only the daemon can
read/write that pipe → the daemon *must* be the fan-out point, which is exactly the hub's
single-session-broadcast model (the daemon owns one `pi` process, broadcasts its events to all
clients). No per-client duplication is even possible.

## Protocol mapping (daemon ⇄ `pi --mode rpc`)
| daemon need | pi RPC |
|---|---|
| user turn | `{"type":"prompt","message":"…"}` (mid-stream: `steer`/`follow_up`) |
| assistant text | `message_update` → `assistantMessageEvent.type:"text_delta"` (`.delta`) |
| thinking | `message_update` → `assistantMessageEvent.type:"thinking_delta"` |
| tool running | `tool_execution_start {toolName, args}` (+ `_update`/`_end`) |
| **approval** | `extension_ui_request {method:"confirm", id, title, message}` — **blocks** until the client sends `extension_ui_response {id, confirmed}` |
| turn/run end | `turn_end` / `agent_end` |
| interrupt | `{"type":"abort"}` |
| session id | `get_state` → `data.sessionId` (resume by starting with `--session-id`/`--session`) |

Framing caveat: pi RPC is **strict LF JSONL** — do NOT use Node `readline` (it splits on U+2028/U+2029).
The Go provider uses `bufio.Scanner` (LF only), which is compliant.

## What's implemented (gated)
`daemon/agent/pi` — `New([]string{"pi","--mode","rpc"})`, `Create`/`Prompt`/`Respond`/`Stop`, and a
readLoop translating the events above into the daemon's `OutputDelta`/`Thinking`/`SessionStatus`/
`ApprovalRequest`. Enabled with **`oculusd serve --pi <path-to-pi>`** (off by default). Approvals map
`allow`/`always` → `confirmed:true`.

## Tested
`daemon/agent/pi/pi_test.go` drives the provider against a **fake `pi --mode rpc`** speaking pi's real
documented event shapes: thinking + text deltas, a `confirm` approval that **blocks** until answered,
then a tool + more text + `agent_end`. Offline — no pi/LLM needed.

## Remaining gate (validate live before relying on it)
- Which tools actually trigger a `confirm` (pi's tool-permission behavior depends on config/extensions;
  the built-in tools may auto-run). Confirm the daemon surfaces the approvals you expect.
- Exact `extension_ui_request` fields for a tool confirm (tool name / title / message) — the provider
  falls back to `tool:"confirm"` + `message`/`title` as detail.
- `Attach`/resume by session id (multi-client attach to an already-owned pi session works via the hub's
  `subscribe` with **no** provider involvement; resume-from-disk is the follow-up).
- Auth/provider: pi defaults to `--provider google`; set the provider/key you use.

Run it: `oculusd serve --pi $(which pi)` then drive a session from the app.
