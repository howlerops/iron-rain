import { For, Show } from "solid-js";
import { ironRainTheme } from "../../theme/theme.js";
import type { ProviderChoice } from "./types.js";
import { PROVIDER_TYPE_LABELS } from "./types.js";

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
      <text fg={ironRainTheme.brand.primary}>
        <b>Select Providers</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Choose which model providers you want to use. Select at least one.
      </text>

      <box marginY={1} />

      <box
        flexDirection="column"
        border
        borderStyle="rounded"
        borderColor={ironRainTheme.chrome.border}
        paddingX={1}
        paddingY={1}
      >
        <For each={props.providers}>
          {(provider, i) => {
            const isActive = () => i() === props.cursorIndex;
            const isFirstOfType = () =>
              i() === 0 || props.providers[i() - 1]?.type !== provider.type;

            return (
              <box flexDirection="column">
                <Show when={isFirstOfType()}>
                  <box paddingX={0} marginTop={i() > 0 ? 1 : 0}>
                    <text fg={ironRainTheme.chrome.muted}>
                      {PROVIDER_TYPE_LABELS[provider.type]}
                    </text>
                  </box>
                </Show>
                <box flexDirection="row" gap={1}>
                  <text
                    fg={
                      isActive()
                        ? ironRainTheme.brand.primary
                        : ironRainTheme.chrome.dimFg
                    }
                  >
                    {isActive() ? "\u25B8" : " "}
                  </text>
                  <text
                    fg={
                      provider.selected
                        ? ironRainTheme.status.success
                        : ironRainTheme.chrome.dimFg
                    }
                  >
                    {provider.selected ? "[x]" : "[ ]"}
                  </text>
                  <text
                    fg={
                      isActive()
                        ? ironRainTheme.chrome.fg
                        : ironRainTheme.chrome.muted
                    }
                  >
                    {isActive() ? <b>{provider.name}</b> : provider.name}
                  </text>
                  <text fg={ironRainTheme.chrome.dimFg}>
                    {provider.description}
                  </text>
                </box>
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <box flexDirection="row" gap={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Space]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>toggle</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Enter]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>next</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Backspace]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>back</text>
      </box>
    </box>
  );
}
