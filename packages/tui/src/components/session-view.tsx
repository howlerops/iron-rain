import type { SlotName } from "@howlerops/iron-rain";
import { SyntaxStyle } from "@opentui/core";
import { createMemo, createSignal, For, onCleanup, Show } from "solid-js";
import { ironRainTheme, slotColor, slotLabel } from "../theme/theme.js";
import type { SubagentActivity, ToolCallEntry } from "./subagent-grid.js";
import { categorizeToolCall, SubagentGrid } from "./subagent-grid.js";

const defaultSyntaxStyle = SyntaxStyle.create();

export type { ToolCallEntry };

export interface SlotActivity {
  slot: SlotName;
  task: string;
  status: "running" | "done" | "error" | "interrupted";
  duration?: number;
  tokens?: number;
  cost?: number;
  toolCalls?: ToolCallEntry[];
}

export interface SessionStats {
  totalDuration: number;
  totalTokens: number;
  modelCount: number;
  requestCount: number;
}

export interface MessageImage {
  path: string;
  name: string;
  sizeKB: number;
}

export interface Message {
  id: string;
  role: "user" | "assistant";
  content: string;
  slot?: SlotName;
  timestamp: number;
  activities?: SlotActivity[];
  tokens?: number;
  duration?: number;
  images?: MessageImage[];
}

export interface SessionViewProps {
  messages: Message[];
  stats?: SessionStats;
  isLoading?: boolean;
  activeSlot?: SlotName;
  streamingContent?: string;
  streamingThinking?: string;
  streamingSystemPrompt?: string;
  streamingToolCalls?: ToolCallEntry[];
  streamingTask?: string;
  loadingStartTime?: number;
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const secs = Math.round(ms / 100) / 10;
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  const remainSecs = Math.round(secs % 60);
  return `${mins}m ${remainSecs}s`;
}

export function formatTokens(n: number): string {
  if (n < 1000) return `${n}`;
  return `${(n / 1000).toFixed(1)}k`;
}

/* ── User message ──────────────────────────────────────────────── */

function UserMessage(props: { message: Message }) {
  return (
    <box flexDirection="column" paddingX={1} marginBottom={1}>
      <box flexDirection="row" gap={1}>
        <text fg={ironRainTheme.brand.primary}>
          <b>&gt;</b>
        </text>
        <text fg={ironRainTheme.chrome.fg}>{props.message.content}</text>
      </box>
      <Show when={props.message.images && props.message.images.length > 0}>
        <box flexDirection="row" gap={1} paddingLeft={2}>
          <For each={props.message.images}>
            {(img) => (
              <box
                border
                borderStyle="rounded"
                borderColor={ironRainTheme.chrome.border}
                paddingX={1}
              >
                <text fg={ironRainTheme.chrome.muted}>
                  {`\uD83D\uDDBC ${img.name} (${img.sizeKB}KB)`}
                </text>
              </box>
            )}
          </For>
        </box>
      </Show>
    </box>
  );
}

/* ── Assistant message ─────────────────────────────────────────── */

/** Convert SlotActivity to SubagentActivity for the grid component */
function toSubagentActivity(a: SlotActivity): SubagentActivity {
  return {
    slot: a.slot,
    task: a.task,
    status: a.status,
    duration: a.duration,
    tokens: a.tokens,
    cost: a.cost,
    toolCalls: a.toolCalls,
  };
}

/** Compact tool call summary for single-agent footer */
function compactToolSummary(toolCalls: ToolCallEntry[]): string {
  if (toolCalls.length === 0) return "";
  const counts: Record<string, number> = {};
  for (const tc of toolCalls) {
    const cat = categorizeToolCall(tc.name);
    counts[cat.label] = (counts[cat.label] ?? 0) + 1;
  }
  return Object.entries(counts)
    .map(([label, count]) => `${count} ${label.toLowerCase()}`)
    .join(", ");
}

function AssistantMessage(props: { message: Message }) {
  const hasActivities = () =>
    props.message.activities && props.message.activities.length > 0;
  const isMultiAgent = () =>
    props.message.activities && props.message.activities.length > 1;

  return (
    <box flexDirection="column" paddingX={1} marginBottom={1}>
      {/* Multi-agent grid for parallel subagent display */}
      <Show when={isMultiAgent()}>
        <SubagentGrid
          activities={props.message.activities!.map(toSubagentActivity)}
          totalDuration={props.message.duration}
        />
      </Show>

      {/* Single-agent activity footer with tool summary */}
      <Show when={hasActivities() && !isMultiAgent()}>
        <box flexDirection="row" gap={1} marginBottom={0}>
          <text fg={slotColor(props.message.activities![0].slot)}>
            {slotLabel(props.message.activities![0].slot)}
          </text>
          <text fg={ironRainTheme.chrome.dimFg}>
            {(() => {
              const a = props.message.activities![0];
              const parts: string[] = [];
              if (a.duration != null) parts.push(formatDuration(a.duration));
              if (a.tokens != null)
                parts.push(`${formatTokens(a.tokens)} tokens`);
              if (a.toolCalls && a.toolCalls.length > 0)
                parts.push(compactToolSummary(a.toolCalls));
              return parts.join(" \u00B7 ");
            })()}
          </text>
        </box>
      </Show>

      {/* Response content with markdown */}
      <markdown
        content={props.message.content}
        syntaxStyle={defaultSyntaxStyle}
      />

      {/* Response stats (for messages without activities) */}
      <Show
        when={
          !hasActivities() &&
          (props.message.tokens != null || props.message.duration != null)
        }
      >
        <box flexDirection="row" gap={1} marginTop={0}>
          <text fg={slotColor(props.message.slot ?? "main")}>
            {slotLabel(props.message.slot ?? "main")}
          </text>
          <text fg={ironRainTheme.chrome.dimFg}>
            {[
              props.message.duration != null
                ? formatDuration(props.message.duration)
                : null,
              props.message.tokens != null
                ? `${formatTokens(props.message.tokens)} tokens`
                : null,
            ]
              .filter(Boolean)
              .join(" \u00B7 ")}
          </text>
        </box>
      </Show>
    </box>
  );
}

/* ── Helpers ────────────────────────────────────────────────────── */

function truncateText(text: string, maxLen: number): string {
  if (text.length <= maxLen) return text;
  return `${text.slice(0, maxLen - 3)}...`;
}

/* ── Streaming agent card ──────────────────────────────────────── */

const SPINNER_FRAMES = [
  "\u2581",
  "\u2582",
  "\u2583",
  "\u2584",
  "\u2585",
  "\u2586",
  "\u2587",
  "\u2588",
  "\u2587",
  "\u2586",
  "\u2585",
  "\u2584",
  "\u2583",
  "\u2582",
];

const THINKING_FRAMES = ["\u25CC", "\u25CB", "\u25CF", "\u25CB"];

function StreamingAgentCard(props: {
  slot: SlotName;
  task: string;
  toolCalls: ToolCallEntry[];
  content: string;
  thinking: string;
  systemPrompt: string;
  startTime: number;
}) {
  const [frame, setFrame] = createSignal(0);
  const [elapsed, setElapsed] = createSignal(0);
  let disposed = false;

  const timer = setInterval(() => {
    if (disposed) return;
    setFrame((f) => (f + 1) % SPINNER_FRAMES.length);
    setElapsed(Math.floor((Date.now() - props.startTime) / 1000));
  }, 80);
  onCleanup(() => {
    disposed = true;
    clearInterval(timer);
  });

  const elapsedStr = () => {
    const s = elapsed();
    if (s < 60) return `${s}s`;
    return `${Math.floor(s / 60)}m ${s % 60}s`;
  };

  const truncTask = () =>
    props.task.length > 50 ? `${props.task.slice(0, 47)}...` : props.task;

  const contentPreview = () => {
    if (!props.content) return "";
    const lines = props.content.replace(/\n+/g, " ").trim();
    return truncateText(lines, 200);
  };

  const thinkingPreview = () => {
    if (!props.thinking) return "";
    const lines = props.thinking.replace(/\n+/g, " ").trim();
    return truncateText(lines, 100);
  };

  const isThinking = () =>
    props.thinking && !props.content && props.toolCalls.length === 0;

  // Count tool calls by status
  const toolStats = createMemo(() => {
    const done = props.toolCalls.filter((t) => t.status === "done").length;
    const running = props.toolCalls.filter(
      (t) => t.status === "running",
    ).length;
    const errored = props.toolCalls.filter((t) => t.status === "error").length;
    return { done, running, errored, total: props.toolCalls.length };
  });

  // Show only last N tool calls to keep card compact
  const visibleToolCalls = createMemo(() => {
    const MAX_VISIBLE = 8;
    const calls = props.toolCalls;
    if (calls.length <= MAX_VISIBLE) return { calls, hidden: 0 };
    return {
      calls: calls.slice(calls.length - MAX_VISIBLE),
      hidden: calls.length - MAX_VISIBLE,
    };
  });

  return (
    <box
      flexDirection="column"
      border
      borderStyle="rounded"
      borderColor={slotColor(props.slot)}
      paddingX={1}
      marginX={1}
      marginBottom={1}
    >
      {/* Header: spinner + slot label + task */}
      <box flexDirection="row" gap={1}>
        <text fg={slotColor(props.slot)}>
          <b>{SPINNER_FRAMES[frame()]}</b>
        </text>
        <text fg={slotColor(props.slot)}>
          <b>{slotLabel(props.slot)}</b>
        </text>
        <Show when={props.task}>
          <text fg={ironRainTheme.chrome.dimFg}>{"\u2014"}</text>
          <text fg={ironRainTheme.chrome.fg} truncate>
            {truncTask()}
          </text>
        </Show>
      </box>

      {/* Thinking indicator */}
      <Show when={isThinking()}>
        <box flexDirection="row" gap={1} paddingLeft={2}>
          <text fg={ironRainTheme.chrome.muted}>
            {THINKING_FRAMES[Math.floor(frame() / 3) % THINKING_FRAMES.length]}
          </text>
          <text fg={ironRainTheme.chrome.muted}>
            {thinkingPreview() || "Reasoning..."}
          </text>
        </box>
      </Show>

      {/* Tool call progress summary when many calls */}
      <Show when={toolStats().total > 0}>
        <box flexDirection="row" gap={1} paddingLeft={2}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`${toolStats().done}/${toolStats().total} tools`}
          </text>
          <Show when={toolStats().errored > 0}>
            <text fg={ironRainTheme.status.error}>
              {`${toolStats().errored} failed`}
            </text>
          </Show>
        </box>
      </Show>

      {/* Categorized tool call tree */}
      <Show when={props.toolCalls.length > 0}>
        <box flexDirection="column" paddingLeft={2}>
          <Show when={visibleToolCalls().hidden > 0}>
            <text fg={ironRainTheme.chrome.dimFg}>
              {`  \u2026 ${visibleToolCalls().hidden} earlier`}
            </text>
          </Show>
          <For each={visibleToolCalls().calls}>
            {(tc, i) => {
              const cat = categorizeToolCall(tc.name);
              const isLast = () => i() === visibleToolCalls().calls.length - 1;
              const connector = () => (isLast() ? "\u2514" : "\u251C");
              const statusChar = () =>
                tc.status === "done"
                  ? "\u2713"
                  : tc.status === "error"
                    ? "\u2717"
                    : "\u2192";
              const statusClr = () =>
                tc.status === "done"
                  ? ironRainTheme.status.success
                  : tc.status === "error"
                    ? ironRainTheme.status.error
                    : ironRainTheme.status.warning;

              return (
                <box flexDirection="row" gap={0}>
                  <text fg={ironRainTheme.chrome.dimFg}>
                    {`${connector()} `}
                  </text>
                  <text fg={cat.color}>{`${cat.icon} `}</text>
                  <text fg={ironRainTheme.chrome.fg} truncate>
                    {`${tc.name} `}
                  </text>
                  <text fg={statusClr()}>{statusChar()}</text>
                </box>
              );
            }}
          </For>
        </box>
      </Show>

      {/* Content preview */}
      <Show when={contentPreview()}>
        <box paddingLeft={2} marginTop={0}>
          <text fg={ironRainTheme.chrome.muted} truncate>
            {contentPreview()}
          </text>
        </box>
      </Show>

      {/* Footer: elapsed · hints */}
      <box flexDirection="row" gap={1} marginTop={0}>
        <text fg={slotColor(props.slot)}>{elapsedStr()}</text>
        <text fg={ironRainTheme.chrome.dimFg}>
          {`\u00B7 esc cancel \u00B7 enter add context`}
        </text>
      </box>
    </box>
  );
}

/* ── Unused but kept for compatibility ─────────────────────────── */

function _CumulativeStats(props: { stats: SessionStats }) {
  return (
    <box flexDirection="row" paddingX={1} marginTop={1}>
      <text fg={ironRainTheme.chrome.dimFg}>
        {`\u2219 ${formatDuration(props.stats.totalDuration)} \u00B7 ${props.stats.requestCount} request${props.stats.requestCount !== 1 ? "s" : ""} \u00B7 ${formatTokens(props.stats.totalTokens)} tokens`}
      </text>
    </box>
  );
}

/* ── SessionView ───────────────────────────────────────────────── */

export function SessionView(props: SessionViewProps) {
  return (
    <box flexDirection="column" flexGrow={1}>
      <For each={props.messages}>
        {(msg) =>
          msg.role === "user" ? (
            <UserMessage message={msg} />
          ) : (
            <AssistantMessage message={msg} />
          )
        }
      </For>

      {/* Streaming agent card */}
      <Show when={props.isLoading}>
        <StreamingAgentCard
          slot={props.activeSlot ?? "main"}
          task={props.streamingTask ?? ""}
          toolCalls={props.streamingToolCalls ?? []}
          content={props.streamingContent ?? ""}
          thinking={props.streamingThinking ?? ""}
          systemPrompt={props.streamingSystemPrompt ?? ""}
          startTime={props.loadingStartTime ?? Date.now()}
        />
      </Show>
    </box>
  );
}
