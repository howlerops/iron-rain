import { For } from 'solid-js';
import type { SlotName } from '@howlerops/iron-rain';
import { ironRainTheme, slotColor, slotLabel } from '../theme/theme.js';

// SyntaxStyle is created lazily at runtime since @opentui/core
// doesn't export it from the package root's type declarations.
let _syntaxStyle: any = null;
function getSyntaxStyle(): any {
  if (!_syntaxStyle) {
    try {
      // @ts-ignore — runtime import, type not available at compile time
      const { SyntaxStyle } = require('@opentui/core/syntax-style');
      _syntaxStyle = SyntaxStyle.create();
    } catch {
      _syntaxStyle = {};
    }
  }
  return _syntaxStyle;
}

export interface SlotActivity {
  slot: SlotName;
  task: string;
  status: 'running' | 'done' | 'error';
  duration?: number;
  tokens?: number;
}

export interface SessionStats {
  totalDuration: number;
  totalTokens: number;
  modelCount: number;
  requestCount: number;
}

export interface Message {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  slot?: SlotName;
  timestamp: number;
  activities?: SlotActivity[];
  tokens?: number;
  duration?: number;
}

export interface SessionViewProps {
  messages: Message[];
  stats?: SessionStats;
  isLoading?: boolean;
  activeSlot?: SlotName;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const secs = Math.round(ms / 100) / 10;
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  const remainSecs = Math.round(secs % 60);
  return `${mins}m ${remainSecs}s`;
}

function formatTokens(n: number): string {
  if (n < 1000) return `${n}`;
  return `${(n / 1000).toFixed(1)}k`;
}

function UserMessage(props: { message: Message }) {
  return (
    <box flexDirection="column" paddingX={1} marginBottom={1}>
      <box flexDirection="row" gap={1}>
        <text fg={ironRainTheme.brand.primary}><b>&gt;</b></text>
        <text fg={ironRainTheme.chrome.fg}>{props.message.content}</text>
      </box>
    </box>
  );
}

function SlotActivityCard(props: { activity: SlotActivity }) {
  const color = slotColor(props.activity.slot);
  const dot = props.activity.status === 'done' ? '\u25CF'
    : props.activity.status === 'error' ? '\u25CF'
    : '\u25CB';
  const dotColor = props.activity.status === 'done' ? ironRainTheme.status.success
    : props.activity.status === 'error' ? ironRainTheme.status.error
    : ironRainTheme.status.warning;

  const truncatedTask = props.activity.task.length > 40
    ? props.activity.task.slice(0, 37) + '...'
    : props.activity.task;

  return (
    <box
      flexDirection="column"
      border
      borderStyle="rounded"
      borderColor={ironRainTheme.chrome.border}
      paddingX={1}
      marginBottom={0}
      minWidth={30}
      maxWidth={50}
    >
      <box flexDirection="row" gap={1}>
        <text fg={dotColor}>{dot}</text>
        <text fg={color}><b>{slotLabel(props.activity.slot)}</b></text>
      </box>
      <text fg={ironRainTheme.chrome.fg} truncate>{truncatedTask}</text>
      {props.activity.status !== 'running' && (
        <box flexDirection="row" gap={2}>
          <text fg={props.activity.status === 'done' ? ironRainTheme.status.success : ironRainTheme.status.error}>
            {props.activity.status === 'done' ? 'Done' : 'Error'}{props.activity.duration != null ? ` in ${formatDuration(props.activity.duration)}` : ''}
          </text>
          {props.activity.tokens != null && (
            <text fg={ironRainTheme.chrome.dimFg}>{formatTokens(props.activity.tokens)} tokens</text>
          )}
        </box>
      )}
    </box>
  );
}

function AssistantMessage(props: { message: Message }) {
  const color = props.message.slot
    ? ironRainTheme.slots[props.message.slot]
    : ironRainTheme.brand.primary;

  const hasActivities = props.message.activities && props.message.activities.length > 0;
  const doneActivities = props.message.activities?.filter(a => a.status !== 'running') ?? [];

  return (
    <box flexDirection="column" paddingX={1} marginBottom={1}>
      {/* Activity cards row */}
      {hasActivities && (
        <box flexDirection="column" marginBottom={1}>
          <text fg={ironRainTheme.chrome.dimFg}>
            Dispatched to {doneActivities.length} slot{doneActivities.length !== 1 ? 's' : ''}
          </text>
          <box flexDirection="row" gap={1} flexWrap="wrap">
            <For each={props.message.activities}>
              {(activity) => <SlotActivityCard activity={activity} />}
            </For>
          </box>
        </box>
      )}

      {/* Response content with markdown */}
      <markdown content={props.message.content} syntaxStyle={getSyntaxStyle()} />

      {/* Response stats */}
      {(props.message.tokens != null || props.message.duration != null) && (
        <box flexDirection="row" gap={2} marginTop={0}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {props.message.slot ? slotLabel(props.message.slot) : 'Main'}
            {props.message.duration != null ? ` \u00B7 ${formatDuration(props.message.duration)}` : ''}
            {props.message.tokens != null ? ` \u00B7 ${formatTokens(props.message.tokens)} tokens` : ''}
          </text>
        </box>
      )}
    </box>
  );
}

function ThinkingIndicator(props: { slot?: SlotName }) {
  const color = props.slot
    ? ironRainTheme.slots[props.slot]
    : ironRainTheme.brand.primary;

  return (
    <box flexDirection="row" gap={1} paddingX={1} marginBottom={1}>
      <text fg={color}><b>{slotLabel(props.slot ?? 'main')}</b></text>
      <text fg={ironRainTheme.chrome.muted}>is thinking...</text>
    </box>
  );
}

function CumulativeStats(props: { stats: SessionStats }) {
  return (
    <box flexDirection="row" paddingX={1} marginTop={1}>
      <text fg={ironRainTheme.chrome.dimFg}>
        {'\u2219'} {formatDuration(props.stats.totalDuration)}
        {' \u00B7 '}{props.stats.requestCount} request{props.stats.requestCount !== 1 ? 's' : ''}
        {' \u00B7 '}{formatTokens(props.stats.totalTokens)} tokens
      </text>
    </box>
  );
}

export function SessionView(props: SessionViewProps) {
  return (
    <box flexDirection="column" flexGrow={1}>
      <For each={props.messages}>
        {(msg) => msg.role === 'user'
          ? <UserMessage message={msg} />
          : <AssistantMessage message={msg} />
        }
      </For>

      {props.isLoading && <ThinkingIndicator slot={props.activeSlot} />}

      {props.stats && props.stats.requestCount > 0 && (
        <CumulativeStats stats={props.stats} />
      )}
    </box>
  );
}
