import { useSlate } from '../context/slate-context.js';
import { SessionView } from '../components/session-view.js';
import { ModelSlotBar } from '../components/model-slot-bar.js';
import { ironRainTheme } from '../theme/theme.js';

export function SessionRoute() {
  const [state] = useSlate();

  return (
    <box flexDirection="column" flexGrow={1}>
      <ModelSlotBar slots={state.slots} activeSlot={state.activeSlot} />
      <box
        flexDirection="column"
        flexGrow={1}
        border
        borderStyle="rounded"
        borderColor={ironRainTheme.chrome.border}
        marginX={1}
      >
        <SessionView messages={state.messages} />
      </box>
      <box paddingX={2} paddingY={0}>
        {state.isLoading ? (
          <text fg={ironRainTheme.slots[state.activeSlot]}>Thinking...</text>
        ) : (
          <text fg={ironRainTheme.chrome.muted}>Type a message or /help for commands</text>
        )}
      </box>
    </box>
  );
}
