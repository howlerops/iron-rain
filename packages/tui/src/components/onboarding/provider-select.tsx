import { For } from 'solid-js';
import { ironRainTheme } from '../../theme/theme.js';
import type { ProviderChoice } from './types.js';

export interface ProviderSelectProps {
  providers: ProviderChoice[];
  cursorIndex: number;
  onToggle: (index: number) => void;
  onNext: () => void;
  onBack: () => void;
}

export function ProviderSelect(props: ProviderSelectProps) {
  return (
    <box flexDirection="column" paddingX={4} paddingY={1}>
      <text color={ironRainTheme.brand.primary} bold>
        Select Providers
      </text>
      <text color={ironRainTheme.chrome.muted}>
        Choose which model providers you want to use. Select at least one.
      </text>

      <box marginY={1} />

      <box flexDirection="column" borderStyle="round" borderColor={ironRainTheme.chrome.border}
        paddingX={1} paddingY={1}>
        <For each={props.providers}>
          {(provider, i) => {
            const isActive = () => i() === props.cursorIndex;
            const typeColor = provider.type === 'local'
              ? ironRainTheme.status.success
              : provider.type === 'cli'
              ? ironRainTheme.slots.explore
              : ironRainTheme.brand.primary;

            return (
              <box flexDirection="row" gap={1}>
                <text color={isActive() ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                  {isActive() ? '>' : ' '}
                </text>
                <text color={provider.selected ? ironRainTheme.status.success : ironRainTheme.chrome.dimFg}>
                  {provider.selected ? '[x]' : '[ ]'}
                </text>
                <text color={isActive() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}
                  bold={isActive()}>
                  {provider.name}
                </text>
                <text color={typeColor} dim>
                  ({provider.type})
                </text>
                <text color={ironRainTheme.chrome.dimFg}>
                  {provider.description}
                </text>
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text color={ironRainTheme.brand.primary} bold>
          [Space] Toggle
        </text>
        <text color={ironRainTheme.brand.primary} bold>
          [Enter] Next
        </text>
        <text color={ironRainTheme.chrome.muted}>
          [Backspace] Back
        </text>
      </box>
    </box>
  );
}
