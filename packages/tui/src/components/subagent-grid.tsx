/**
 * SubagentGrid — Responsive grid of subagent cards for multi-agent workflows.
 *
 * Inspired by Slate's subagent display: each card shows a status dot,
 * task title, tool-call tree, and footer with duration/tokens/cost.
 * The grid uses flexWrap to adapt columns to terminal width.
 */
import { For, Show } from 'solid-js';
import type { SlotName } from '@howlerops/iron-rain';
import { ironRainTheme, slotColor, slotLabel } from '../theme/theme.js';

/* ── Data types ─────────────────────────────────────────────────── */

export interface ToolCallEntry {
  name: string;
  status: 'running' | 'done' | 'error';
}

export interface SubagentActivity {
  slot: SlotName;
  task: string;
  status: 'running' | 'done' | 'error' | 'interrupted';
  duration?: number;
  tokens?: number;
  cost?: number;
  toolCalls?: ToolCallEntry[];
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

/* ── SubagentCard ───────────────────────────────────────────────── */

function statusDot(status: SubagentActivity['status']): { char: string; color: string } {
  switch (status) {
    case 'done':        return { char: '\u25CF', color: ironRainTheme.status.success };
    case 'error':       return { char: '\u25CF', color: ironRainTheme.status.error };
    case 'running':     return { char: '\u25CB', color: ironRainTheme.status.warning };
    case 'interrupted': return { char: '\u25CF', color: ironRainTheme.chrome.muted };
  }
}

function statusLabel(a: SubagentActivity): string {
  switch (a.status) {
    case 'done':        return `Done${a.duration != null ? ` in ${fmtDuration(a.duration)}` : ''}`;
    case 'error':       return `Error${a.duration != null ? ` after ${fmtDuration(a.duration)}` : ''}`;
    case 'interrupted': return `Interrupted${a.duration != null ? ` after ${fmtDuration(a.duration)}` : ''}`;
    case 'running':     return 'Running...';
  }
}

function statusColor(status: SubagentActivity['status']): string {
  switch (status) {
    case 'done':        return ironRainTheme.status.success;
    case 'error':       return ironRainTheme.status.error;
    case 'interrupted': return ironRainTheme.chrome.muted;
    case 'running':     return ironRainTheme.status.warning;
  }
}

export function SubagentCard(props: { activity: SubagentActivity }) {
  const a = () => props.activity;
  const dot = () => statusDot(a().status);
  const truncTitle = () =>
    a().task.length > 35 ? a().task.slice(0, 32) + '...' : a().task;

  return (
    <box
      flexDirection="column"
      border
      borderStyle="rounded"
      borderColor={ironRainTheme.chrome.border}
      paddingX={1}
      flexGrow={1}
      minWidth={30}
    >
      {/* Header: dot + slot label + task title */}
      <box flexDirection="row" gap={1}>
        <text fg={dot().color}>{dot().char}</text>
        <text fg={slotColor(a().slot)}><b>{slotLabel(a().slot)}:</b></text>
        <text fg={ironRainTheme.chrome.fg} truncate><b>{truncTitle()}</b></text>
      </box>

      {/* Tool call tree */}
      <Show when={a().toolCalls && a().toolCalls!.length > 0}>
        <For each={a().toolCalls}>
          {(tc, i) => {
            const isLast = () => i() === a().toolCalls!.length - 1;
            const connector = () => isLast() ? '\u2514' : '\u251C';
            const check = () =>
              tc.status === 'done' ? '\u2713'
              : tc.status === 'error' ? '\u2717'
              : '\u2026';
            const checkColor = () =>
              tc.status === 'done' ? ironRainTheme.status.success
              : tc.status === 'error' ? ironRainTheme.status.error
              : ironRainTheme.chrome.muted;

            return (
              <box flexDirection="row" gap={0} paddingLeft={1}>
                <text fg={ironRainTheme.chrome.dimFg}>{connector()} </text>
                <text fg={ironRainTheme.chrome.fg} truncate>{tc.name} </text>
                <text fg={checkColor()}>{check()}</text>
              </box>
            );
          }}
        </For>
      </Show>

      {/* Footer: status, tokens, cost */}
      <box flexDirection="row" gap={2} marginTop={0}>
        <text fg={statusColor(a().status)}>
          {statusLabel(a())}
        </text>
        <Show when={a().tokens != null}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`${fmtTokens(a().tokens!)} tokens`}
          </text>
        </Show>
        <Show when={a().cost != null}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {fmtCost(a().cost!)}
          </text>
        </Show>
      </box>
    </box>
  );
}

/* ── Summary helpers ─────────────────────────────────────────────── */

function buildSummaryText(activities: SubagentActivity[], totalDuration?: number): string {
  const done = activities.filter(a => a.status === 'done').length;
  const interrupted = activities.filter(a => a.status === 'interrupted').length;
  const errored = activities.filter(a => a.status === 'error').length;
  const parts: string[] = [];
  if (done > 0) parts.push(`${done} completed`);
  if (interrupted > 0) parts.push(`${interrupted} interrupted`);
  if (errored > 0) parts.push(`${errored} errored`);
  let msg = `Finished running ${activities.length} subagent${activities.length !== 1 ? 's' : ''}`;
  if (parts.length > 0) msg += `, ${parts.join(', ')}`;
  if (totalDuration != null) msg += ` (${fmtDuration(totalDuration)})`;
  return msg;
}

/* ── SubagentSummary ────────────────────────────────────────────── */

export function SubagentSummary(props: {
  activities: SubagentActivity[];
}) {
  const total = () => props.activities.length;
  const running = () => props.activities.filter(a => a.status === 'running').length;
  const check = () => running() === 0 ? ' \u2713' : '';

  return (
    <box flexDirection="row" gap={1} marginBottom={0}>
      <text fg={ironRainTheme.chrome.fg}>
        {`Ran ${total()} subagent${total() !== 1 ? 's' : ''}${check()}`}
      </text>
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
      <SubagentSummary activities={props.activities} />
      <box flexDirection="row" flexWrap="wrap" gap={1}>
        <For each={props.activities}>
          {(activity) => <SubagentCard activity={activity} />}
        </For>
      </box>
      <Show when={props.activities.every(a => a.status !== 'running')}>
        <box flexDirection="row" marginTop={0}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {buildSummaryText(props.activities, props.totalDuration)}
          </text>
        </box>
      </Show>
    </box>
  );
}
