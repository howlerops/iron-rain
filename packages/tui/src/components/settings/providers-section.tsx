import { For, Show } from "solid-js";
import { ironRainTheme } from "../../theme/theme.js";
import { PROVIDER_TYPE_LABELS } from "../onboarding/types.js";

export interface ProviderListItem {
  id: string;
  name: string;
  description: string;
  type: "api" | "cli" | "local";
  requiresKey: boolean;
  configured: boolean;
  apiKey?: string;
  apiBase?: string;
  defaultApiBase?: string;
}

export interface ProvidersSectionProps {
  providers: ProviderListItem[];
  cursor: number;
  editingKey: boolean;
  keyBuffer: string;
}

export function ProvidersSection(props: ProvidersSectionProps) {
  return (
    <box flexDirection="column">
      <text fg={ironRainTheme.chrome.muted}>
        Enable providers and configure API keys. Press Enter to toggle.
      </text>
      <box marginY={0} />
      <For each={props.providers}>
        {(prov, i) => {
          const isActive = () => i() === props.cursor;
          const icon = prov.configured ? "\u2713" : "\u25CB";
          const iconColor = prov.configured
            ? ironRainTheme.status.success
            : ironRainTheme.chrome.dimFg;
          const isFirstOfType = () =>
            i() === 0 || props.providers[i() - 1]?.type !== prov.type;
          return (
            <box flexDirection="column">
              <Show when={isFirstOfType()}>
                <box paddingX={1} marginTop={i() > 0 ? 1 : 0}>
                  <text fg={ironRainTheme.chrome.muted}>
                    {PROVIDER_TYPE_LABELS[prov.type]}
                  </text>
                </box>
              </Show>
              <box flexDirection="row" gap={1} paddingX={1}>
                <text
                  fg={
                    isActive()
                      ? ironRainTheme.brand.primary
                      : ironRainTheme.chrome.dimFg
                  }
                >
                  {isActive() ? "\u25B8" : " "}
                </text>
                <text fg={iconColor}>{icon}</text>
                <text
                  fg={
                    isActive()
                      ? ironRainTheme.chrome.fg
                      : ironRainTheme.chrome.muted
                  }
                >
                  {isActive() ? <b>{prov.name}</b> : prov.name}
                </text>
                <text fg={ironRainTheme.chrome.dimFg}>{prov.description}</text>
              </box>
              <Show when={prov.configured && prov.apiKey}>
                <box flexDirection="row" gap={1} paddingX={4}>
                  <text fg={ironRainTheme.chrome.dimFg}>key:</text>
                  <text fg={ironRainTheme.status.success}>
                    {prov.apiKey?.startsWith("env:")
                      ? prov.apiKey!
                      : `${prov.apiKey?.slice(0, 8)}...`}
                  </text>
                </box>
              </Show>
              <Show when={isActive() && props.editingKey}>
                <box flexDirection="row" gap={1} paddingX={4}>
                  <text fg={ironRainTheme.brand.primary}>API key:</text>
                  <text
                    fg={
                      props.keyBuffer
                        ? ironRainTheme.chrome.fg
                        : ironRainTheme.chrome.muted
                    }
                  >
                    {props.keyBuffer || "type key or env:VAR_NAME"}
                  </text>
                  <text fg={ironRainTheme.chrome.muted}>{"\u2588"}</text>
                </box>
                <box flexDirection="column" paddingX={4}>
                  <box flexDirection="row" gap={1}>
                    <text fg={ironRainTheme.chrome.muted}>
                      <b>[Enter]</b>
                    </text>
                    <text fg={ironRainTheme.chrome.dimFg}>save</text>
                    <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
                    <text fg={ironRainTheme.chrome.muted}>
                      <b>[Esc]</b>
                    </text>
                    <text fg={ironRainTheme.chrome.dimFg}>cancel</text>
                  </box>
                  <text fg={ironRainTheme.chrome.dimFg}>
                    Tip: use env:ANTHROPIC_API_KEY
                  </text>
                </box>
              </Show>
            </box>
          );
        }}
      </For>
    </box>
  );
}
