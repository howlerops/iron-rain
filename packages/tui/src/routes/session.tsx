import { createSignal } from 'solid-js';
import { useSlate } from '../context/slate-context.js';
import { SessionView } from '../components/session-view.js';
import { ironRainTheme, slotLabel } from '../theme/theme.js';

export function SessionRoute(props: { version?: string }) {
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
    actions.dispatch(text);
  }

  return (
    <box flexDirection="column" flexGrow={1}>
      {/* Messages area — no border, full width */}
      <scrollbox
        flexGrow={1}
        stickyScroll
        stickyStart="bottom"
        paddingX={1}
      >
        <SessionView
          messages={state.messages}
          stats={state.sessionStats}
          isLoading={state.isLoading}
          activeSlot={state.activeSlot}
        />
      </scrollbox>

      {/* Separator line */}
      <box paddingX={1}>
        <text fg={ironRainTheme.chrome.border}>{'─'.repeat(200)}</text>
      </box>

      {/* Input */}
      <box paddingX={1}>
        <input
          width="100%"
          focused={inputFocused()}
          value={inputValue()}
          placeholder={state.isLoading ? 'Waiting for response...' : 'Send a message...'}
          placeholderColor={ironRainTheme.chrome.muted}
          textColor={ironRainTheme.chrome.fg}
          backgroundColor={ironRainTheme.chrome.bg}
          focusedBackgroundColor={ironRainTheme.chrome.bg}
          focusedTextColor={ironRainTheme.chrome.fg}
          onSubmit={handleSubmit as any}
          onInput={setInputValue}
        />
      </box>

      {/* Tooltip bar */}
      <box flexDirection="row" justifyContent="space-between" paddingX={1}>
        <box flexDirection="row" gap={2}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {state.slots.main.model}
          </text>
          {state.isLoading && (
            <text fg={ironRainTheme.brand.primary}>
              {slotLabel(state.activeSlot)} working...
            </text>
          )}
        </box>
        <box flexDirection="row" gap={2}>
          <text fg={ironRainTheme.chrome.dimFg}>
            <b>Ctrl+C</b> quit
          </text>
          <text fg={ironRainTheme.chrome.dimFg}>
            iron-rain {props.version ?? ''}
          </text>
        </box>
      </box>
    </box>
  );
}
