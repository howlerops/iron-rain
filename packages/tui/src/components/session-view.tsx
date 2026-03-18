import type { SlotName } from "@howlerops/iron-rain";
import { SyntaxStyle } from "@opentui/core";
import { createSignal, For, onCleanup, Show } from "solid-js";
import { ironRainTheme, slotColor, slotLabel } from "../theme/theme.js";
import type { SubagentActivity, ToolCallEntry } from "./subagent-grid.js";
import { SubagentGrid } from "./subagent-grid.js";

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

      {/* Single-agent activity (simple inline display) */}
      <Show when={hasActivities() && !isMultiAgent()}>
        <box flexDirection="row" gap={1} marginBottom={0}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {(() => {
              const a = props.message.activities![0];
              const label = slotLabel(a.slot);
              const dur =
                a.duration != null ? ` in ${formatDuration(a.duration)}` : "";
              const tok =
                a.tokens != null
                  ? ` \u00B7 ${formatTokens(a.tokens)} tokens`
                  : "";
              return `${label}${dur}${tok}`;
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
        <box flexDirection="row" gap={2} marginTop={0}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`${props.message.slot ? slotLabel(props.message.slot) : "Cortex"}${props.message.duration != null ? ` \u00B7 ${formatDuration(props.message.duration)}` : ""}${props.message.tokens != null ? ` \u00B7 ${formatTokens(props.message.tokens)} tokens` : ""}`}
          </text>
        </box>
      </Show>
    </box>
  );
}

function truncateText(text: string, maxLen: number): string {
  if (text.length <= maxLen) return text;
  return `${text.slice(0, maxLen - 3)}...`;
}

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
    props.task.length > 40 ? `${props.task.slice(0, 37)}...` : props.task;

  const contentPreview = () => {
    if (!props.content) return "";
    const lines = props.content.replace(/\n+/g, " ").trim();
    return truncateText(lines, 120);
  };

  return (
    <box
      flexDirection="column"
      border
      borderStyle="rounded"
      borderColor={ironRainTheme.chrome.border}
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
          <b>{slotLabel(props.slot)}:</b>
        </text>
        <text fg={ironRainTheme.chrome.fg} truncate>
          {truncTask()}
        </text>
      </box>

      {/* Tool call tree */}
      <Show when={props.toolCalls.length > 0}>
        <For each={props.toolCalls}>
          {(tc, i) => {
            const isLast = () => i() === props.toolCalls.length - 1;
            const connector = () => (isLast() ? "\u2514" : "\u251C");
            const check = () =>
              tc.status === "done"
                ? "\u2713"
                : tc.status === "error"
                  ? "\u2717"
                  : "\u2192";
            const checkColor = () =>
              tc.status === "done"
                ? ironRainTheme.status.success
                : tc.status === "error"
                  ? ironRainTheme.status.error
                  : ironRainTheme.status.warning;

            return (
              <box flexDirection="row" gap={0} paddingLeft={1}>
                <text fg={ironRainTheme.chrome.dimFg}>{connector()} </text>
                <text fg={ironRainTheme.chrome.fg} truncate>
                  {tc.name}{" "}
                </text>
                <text fg={checkColor()}>{check()}</text>
              </box>
            );
          }}
        </For>
      </Show>

      {/* Content preview */}
      <Show when={contentPreview()}>
        <box paddingLeft={1} marginTop={0}>
          <text fg={ironRainTheme.chrome.muted}>{`> ${contentPreview()}`}</text>
        </box>
      </Show>

      {/* Footer: status + elapsed + hints */}
      <box flexDirection="row" gap={2} marginTop={0}>
        <text fg={ironRainTheme.status.warning}>Running...</text>
        <text fg={ironRainTheme.chrome.dimFg}>
          {`${elapsedStr()} \u00B7 esc to cancel \u00B7 enter to add context`}
        </text>
      </box>
    </box>
  );
}

function _CumulativeStats(props: { stats: SessionStats }) {
  return (
    <box flexDirection="row" paddingX={1} marginTop={1}>
      <text fg={ironRainTheme.chrome.dimFg}>
        {`\u2219 ${formatDuration(props.stats.totalDuration)} \u00B7 ${props.stats.requestCount} request${props.stats.requestCount !== 1 ? "s" : ""} \u00B7 ${formatTokens(props.stats.totalTokens)} tokens`}
      </text>
    </box>
  );
}

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
