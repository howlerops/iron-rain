import { createSignal } from 'solid-js';
import { useKeyboard } from '@opentui/solid';
import { useSlate } from '../context/slate-context.js';
import { SessionView } from '../components/session-view.js';
import { ModelSlotBar } from '../components/model-slot-bar.js';
import { ironRainTheme } from '../theme/theme.js';

export function SessionRoute() {
  const [state, actions] = useSlate();
  const [inputValue, setInputValue] = createSignal('');
  const [inputFocused, setInputFocused] = createSignal(true);

  function handleSubmit(value: string) {
    const text = value.trim();
    if (!text) return;

    actions.addMessage({
      id: crypto.randomUUID?.() ?? `${Date.now()}`,
      role: 'user',
      content: text,
      slot: 'main',
      timestamp: Date.now(),
    });

    setInputValue('');

    // Dispatch to orchestrator kernel — Main slot handles everything
    actions.dispatch(text);
  }

  // Global keyboard shortcuts
  useKeyboard((e) => {
    if (e.ctrl && e.name === 'c') {
      process.exit(0);
    }
  });

  return (
    <box flexDirection="column" flexGrow={1}>
      <ModelSlotBar slots={state.slots} activeSlot={state.activeSlot} />
      <scrollbox
        flexGrow={1}
        border
        borderStyle="rounded"
        borderColor={ironRainTheme.chrome.border}
        marginX={1}
        stickyScroll
        stickyStart="bottom"
      >
        <SessionView messages={state.messages} />
      </scrollbox>
      <box paddingX={1} paddingBottom={0}>
        {state.isLoading ? (
          <text fg={ironRainTheme.slots[state.activeSlot]}>Thinking...</text>
        ) : (
          <input
            width="100%"
            focused={inputFocused()}
            value={inputValue()}
            placeholder="Type a message... (Ctrl+C to quit)"
            placeholderColor={ironRainTheme.chrome.muted}
            textColor={ironRainTheme.chrome.fg}
            backgroundColor={ironRainTheme.chrome.bg}
            focusedBackgroundColor="#1a1a1a"
            focusedTextColor={ironRainTheme.chrome.fg}
            onSubmit={handleSubmit as any}
            onInput={setInputValue}
          />
        )}
      </box>
    </box>
  );
}
