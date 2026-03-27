/**
 * SubagentGrid — Responsive grid of subagent cards for multi-agent workflows.
 *
 * Each card shows a status indicator, task title, categorized tool calls
 * with type-specific icons, and a footer with duration/tokens/cost.
 */

import type { SlotName } from "@howlerops/iron-rain";
import { For, Show } from "solid-js";
import { ironRainTheme, slotColor, slotLabel } from "../theme/theme.js";

/* ── Data types ─────────────────────────────────────────────────── */

export interface ToolCallEntry {
  name: string;
  status: "running" | "done" | "error";
}

export interface SubagentActivity {
  slot: SlotName;
  task: string;
  status: "running" | "done" | "error" | "interrupted";
  duration?: number;
  tokens?: number;
  cost?: number;
  toolCalls?: ToolCallEntry[];
}

/* ── Tool call categorization ─────────────────────────────────── */

interface ToolCategory {
  icon: string;
  color: string;
  label: string;
}

const TOOL_CATEGORIES: Record<string, ToolCategory> = {
  read: { icon: "\u25B7", color: ironRainTheme.brand.lightGold, label: "Read" },
  edit: {
    icon: "\u25C7",
    color: ironRainTheme.brand.accent,
    label: "Edit",
  },
  write: {
    icon: "\u25C6",
    color: ironRainTheme.brand.accent,
    label: "Write",
  },
  bash: {
    icon: "\u25B6",
    color: ironRainTheme.slots.execute,
    label: "Bash",
  },
  search: {
    icon: "\u25C9",
    color: ironRainTheme.brand.lightGold,
    label: "Search",
  },
  thinking: {
    icon: "\u25CC",
    color: ironRainTheme.chrome.muted,
    label: "Thinking",
  },
  dispatch: {
    icon: "\u25E6",
    color: ironRainTheme.brand.primary,
    label: "Dispatch",
  },
  default: {
    icon: "\u2022",
    color: ironRainTheme.chrome.fg,
    label: "Tool",
  },
};

export function categorizeToolCall(name: string): ToolCategory {
  const lower = name.toLowerCase();
  if (
    lower.includes("read") ||
    lower.includes("glob") ||
    lower.includes("grep")
  )
    return TOOL_CATEGORIES.search;
  if (lower.includes("edit")) return TOOL_CATEGORIES.edit;
  if (lower.includes("write")) return TOOL_CATEGORIES.write;
  if (
    lower.includes("bash") ||
    lower.includes("exec") ||
    lower.includes("shell")
  )
    return TOOL_CATEGORIES.bash;
  if (lower.includes("thinking") || lower.includes("think"))
    return TOOL_CATEGORIES.thinking;
  if (lower.includes("dispatch")) return TOOL_CATEGORIES.dispatch;
  return TOOL_CATEGORIES.default;
}

/* ── Formatting helpers ─────────────────────────────────────────── */

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const secs = Math.round(ms / 100) / 10;
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  const rem = Math.round(secs % 60);
  return `${mins}m ${rem}s`;
}

function fmtTokens(n: number): string {
  if (n < 1000) return `${n}`;
  return `${(n / 1000).toFixed(0)}k`;
}

function fmtCost(c: number): string {
  if (c < 0.01) return `$${c.toFixed(4)}`;
  return `$${c.toFixed(2)}`;
}

/* ── Status helpers ────────────────────────────────────────────── */

function statusIndicator(status: SubagentActivity["status"]): {
  char: string;
  color: string;
} {
  switch (status) {
    case "done":
      return { char: "\u2713", color: ironRainTheme.status.success };
    case "error":
      return { char: "\u2717", color: ironRainTheme.status.error };
    case "running":
      return { char: "\u25CB", color: ironRainTheme.status.warning };
    case "interrupted":
      return { char: "\u2014", color: ironRainTheme.chrome.muted };
  }
}

function statusLabel(a: SubagentActivity): string {
  switch (a.status) {
    case "done":
      return a.duration != null ? fmtDuration(a.duration) : "Done";
    case "error":
      return `Error${a.duration != null ? ` ${fmtDuration(a.duration)}` : ""}`;
    case "interrupted":
      return "Interrupted";
    case "running":
      return "Running...";
  }
}

function statusColor(status: SubagentActivity["status"]): string {
  switch (status) {
    case "done":
      return ironRainTheme.chrome.dimFg;
    case "error":
      return ironRainTheme.status.error;
    case "interrupted":
      return ironRainTheme.chrome.muted;
    case "running":
      return ironRainTheme.status.warning;
  }
}

/* ── Tool call summary ─────────────────────────────────────────── */

function toolCallSummary(toolCalls: ToolCallEntry[]): string {
  const done = toolCalls.filter((t) => t.status === "done").length;
  const errored = toolCalls.filter((t) => t.status === "error").length;
  const running = toolCalls.filter((t) => t.status === "running").length;
  const parts: string[] = [];
  if (done > 0) parts.push(`${done} done`);
  if (errored > 0) parts.push(`${errored} failed`);
  if (running > 0) parts.push(`${running} running`);
  return parts.join(", ");
}

/* ── SubagentCard ───────────────────────────────────────────────── */

export function SubagentCard(props: { activity: SubagentActivity }) {
  const a = () => props.activity;
  const indicator = () => statusIndicator(a().status);
  const truncTitle = () =>
    a().task.length > 40 ? `${a().task.slice(0, 37)}...` : a().task;

  return (
    <box
      flexDirection="column"
      border
      borderStyle="rounded"
      borderColor={
        a().status === "error"
          ? ironRainTheme.status.error
          : a().status === "done"
            ? ironRainTheme.chrome.border
            : ironRainTheme.status.warning
      }
      paddingX={1}
      flexGrow={1}
      minWidth={30}
    >
      {/* Header: status + slot label + task title */}
      <box flexDirection="row" gap={1}>
        <text fg={indicator().color}>{indicator().char}</text>
        <text fg={slotColor(a().slot)}>
          <b>{slotLabel(a().slot)}</b>
        </text>
        <text fg={ironRainTheme.chrome.fg} truncate>
          {truncTitle()}
        </text>
      </box>

      {/* Categorized tool call list */}
      <Show when={a().toolCalls && a().toolCalls!.length > 0}>
        <box flexDirection="column" paddingLeft={2} marginTop={0}>
          <For each={a().toolCalls}>
            {(tc, i) => {
              const cat = categorizeToolCall(tc.name);
              const isLast = () => i() === a().toolCalls!.length - 1;
              const connector = () => (isLast() ? "\u2514" : "\u2502");
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

      {/* Footer: status · tokens · cost */}
      <box flexDirection="row" gap={1} marginTop={0}>
        <text fg={statusColor(a().status)}>{statusLabel(a())}</text>
        <Show when={a().toolCalls && a().toolCalls!.length > 0}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`\u00B7 ${a().toolCalls!.length} tools`}
          </text>
        </Show>
        <Show when={a().tokens != null}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`\u00B7 ${fmtTokens(a().tokens!)}tok`}
          </text>
        </Show>
        <Show when={a().cost != null}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`\u00B7 ${fmtCost(a().cost!)}`}
          </text>
        </Show>
      </box>
    </box>
  );
}

/* ── SubagentSummary ────────────────────────────────────────────── */

export function SubagentSummary(props: {
  activities: SubagentActivity[];
  totalDuration?: number;
}) {
  const total = () => props.activities.length;
  const done = () => props.activities.filter((a) => a.status === "done").length;
  const allDone = () => done() === total();

  return (
    <box flexDirection="row" gap={1} marginBottom={0}>
      <text fg={ironRainTheme.brand.primary}>
        <b>
          {allDone()
            ? `\u2713 ${total()} agent${total() !== 1 ? "s" : ""}`
            : `\u25CB ${done()}/${total()} agents`}
        </b>
      </text>
      <Show when={props.totalDuration != null}>
        <text fg={ironRainTheme.chrome.dimFg}>
          {fmtDuration(props.totalDuration!)}
        </text>
      </Show>
      <Show when={!allDone()}>
        {(() => {
          const allToolCalls = props.activities.flatMap(
            (a) => a.toolCalls ?? [],
          );
          const summary = toolCallSummary(allToolCalls);
          return summary ? (
            <text fg={ironRainTheme.chrome.dimFg}>{`(${summary})`}</text>
          ) : null;
        })()}
      </Show>
    </box>
  );
}

/* ── SubagentGrid ───────────────────────────────────────────────── */

export function SubagentGrid(props: {
  activities: SubagentActivity[];
  totalDuration?: number;
}) {
  return (
    <box flexDirection="column" marginBottom={1}>
      <SubagentSummary
        activities={props.activities}
        totalDuration={props.totalDuration}
      />
      <box flexDirection="row" flexWrap="wrap" gap={1}>
        <For each={props.activities}>
          {(activity) => <SubagentCard activity={activity} />}
        </For>
      </box>
    </box>
  );
}
