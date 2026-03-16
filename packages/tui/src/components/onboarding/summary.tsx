import { For } from 'solid-js';
import type { SlotName, SlotConfig } from '@howlerops/iron-rain';
import { SLOT_NAMES } from '@howlerops/iron-rain';
import { ironRainTheme, slotColor, slotLabel } from '../../theme/theme.js';
import type { ProviderChoice } from './types.js';

export interface SummaryProps {
  providers: ProviderChoice[];
  credentials: Record<string, { apiKey?: string; apiBase?: string }>;
  slots: Record<SlotName, SlotConfig>;
  configPath: string;
  onSave: () => void;
  onBack: () => void;
}

export function Summary(props: SummaryProps) {
  const selectedProviders = () => props.providers.filter(p => p.selected);

  return (
    <box flexDirection="column" paddingX={4} paddingY={1}>
      <text color={ironRainTheme.brand.primary} bold>
        Review Configuration
      </text>
      <text color={ironRainTheme.chrome.muted}>
        Review your setup before saving.
      </text>

      <box marginY={1} />

      {/* Providers */}
      <box flexDirection="column" borderStyle="round" borderColor={ironRainTheme.chrome.border}
        paddingX={1} paddingY={1}>
        <text color={ironRainTheme.brand.lightGold} bold>Providers</text>
        <For each={selectedProviders()}>
          {(provider) => {
            const cred = () => props.credentials[provider.id] ?? {};
            return (
              <box flexDirection="row" gap={1} paddingX={1}>
                <text color={ironRainTheme.status.success}>+</text>
                <text color={ironRainTheme.chrome.fg}>{provider.name}</text>
                <text color={ironRainTheme.chrome.dimFg}>
                  ({provider.type})
                </text>
                {cred().apiKey && (
                  <text color={ironRainTheme.chrome.muted}>
                    key: {cred().apiKey!.startsWith('env:') ? cred().apiKey! : '***'}
                  </text>
                )}
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      {/* Slot assignments */}
      <box flexDirection="column" borderStyle="round" borderColor={ironRainTheme.chrome.border}
        paddingX={1} paddingY={1}>
        <text color={ironRainTheme.brand.lightGold} bold>Model Slots</text>
        <For each={[...SLOT_NAMES]}>
          {(slot) => {
            const config = () => props.slots[slot];
            return (
              <box flexDirection="row" gap={1} paddingX={1}>
                <text color={slotColor(slot)} bold>
                  {slotLabel(slot).padEnd(8)}
                </text>
                <text color={ironRainTheme.chrome.fg}>
                  {config().provider}/{config().model}
                </text>
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <text color={ironRainTheme.chrome.muted}>
        Config will be saved to: {props.configPath}
      </text>

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text color={ironRainTheme.status.success} bold>
          [Enter] Save & Start
        </text>
        <text color={ironRainTheme.chrome.muted}>
          [Backspace] Back
        </text>
      </box>
    </box>
  );
}
