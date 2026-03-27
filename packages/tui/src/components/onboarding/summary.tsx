import type { SlotConfig, SlotName } from "@howlerops/iron-rain";
import { SLOT_NAMES } from "@howlerops/iron-rain";
import { For } from "solid-js";
import { ironRainTheme, slotColor, slotLabel } from "../../theme/theme.js";
import type { ProviderChoice } from "./types.js";

export interface SummaryProps {
  providers: ProviderChoice[];
  credentials: Record<string, { apiKey?: string; apiBase?: string }>;
  slots: Record<SlotName, SlotConfig>;
  configPath: string;
  onSave: () => void;
  onBack: () => void;
}

export function Summary(props: SummaryProps) {
  const selectedProviders = () => props.providers.filter((p) => p.selected);

  return (
    <box flexDirection="column" paddingX={4} paddingY={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Review Configuration</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Review your setup before saving.
      </text>

      <box marginY={1} />

      {/* Providers */}
      <box
        flexDirection="column"
        border
        borderStyle="rounded"
        borderColor={ironRainTheme.chrome.border}
        paddingX={1}
        paddingY={1}
      >
        <text fg={ironRainTheme.brand.lightGold}>
          <b>Providers</b>
        </text>
        <For each={selectedProviders()}>
          {(provider) => {
            const cred = () => props.credentials[provider.id] ?? {};
            return (
              <box flexDirection="row" gap={1} paddingX={1}>
                <text fg={ironRainTheme.status.success}>+</text>
                <text fg={ironRainTheme.chrome.fg}>{provider.name}</text>
                <text fg={ironRainTheme.chrome.dimFg}>({provider.type})</text>
                {cred().apiKey && (
                  <text fg={ironRainTheme.chrome.muted}>
                    {`key: ${cred().apiKey?.startsWith("env:") ? cred().apiKey! : "***"}`}
                  </text>
                )}
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      {/* Slot assignments */}
      <box
        flexDirection="column"
        border
        borderStyle="rounded"
        borderColor={ironRainTheme.chrome.border}
        paddingX={1}
        paddingY={1}
      >
        <text fg={ironRainTheme.brand.lightGold}>
          <b>Model Slots</b>
        </text>
        <For each={[...SLOT_NAMES]}>
          {(slot) => {
            const config = () => props.slots[slot];
            return (
              <box flexDirection="row" gap={1} paddingX={1}>
                <text fg={slotColor(slot)}>
                  <b>{slotLabel(slot).padEnd(8)}</b>
                </text>
                <text fg={ironRainTheme.chrome.fg}>
                  {`${config().provider}/${config().model}`}
                </text>
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <text fg={ironRainTheme.chrome.muted}>
        {`Config will be saved to: ${props.configPath}`}
      </text>

      <box marginY={1} />

      <box flexDirection="row" gap={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Enter]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>save & start</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Backspace]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>back</text>
      </box>
    </box>
  );
}
