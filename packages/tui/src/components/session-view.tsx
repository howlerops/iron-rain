import type { SlotName } from '@howlerops/iron-rain';
import { ironRainTheme, slotLabel } from '../theme/theme.js';

export interface Message {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  slot?: SlotName;
  timestamp: number;
}

export interface SessionViewProps {
  messages: Message[];
}

function MessageBubble(props: { message: Message }) {
  const isUser = props.message.role === 'user';
  const borderColor = isUser
    ? ironRainTheme.chrome.border
    : props.message.slot
      ? ironRainTheme.slots[props.message.slot]
      : ironRainTheme.brand.primary;

  return (
    <box flexDirection="column" paddingX={1} marginBottom={1}>
      <box flexDirection="row" gap={1}>
        <text fg={borderColor}>
          <b>{isUser ? 'You' : slotLabel(props.message.slot ?? 'main')}</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>
          {new Date(props.message.timestamp).toLocaleTimeString()}
        </text>
      </box>
      <text fg={ironRainTheme.chrome.fg}>{props.message.content}</text>
    </box>
  );
}

export function SessionView(props: SessionViewProps) {
  return (
    <box flexDirection="column" flexGrow={1}>
      {props.messages.map((msg) => (
        <MessageBubble message={msg} />
      ))}
    </box>
  );
}
