import { Show, For } from 'solid-js';
import { ironRainTheme } from '../../theme/theme.js';

export interface ProviderListItem {
  id: string;
  name: string;
  description: string;
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
          const icon = prov.configured ? '\u2713' : '\u25CB';
          const iconColor = prov.configured ? ironRainTheme.status.success : ironRainTheme.chrome.dimFg;
          return (
            <box flexDirection="column">
              <box flexDirection="row" gap={1} paddingX={1}>
                <text fg={isActive() ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                  {isActive() ? '\u25B8' : ' '}
                </text>
                <text fg={iconColor}>{icon}</text>
                <text fg={isActive() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                  {isActive() ? <b>{prov.name}</b> : prov.name}
                </text>
                <text fg={ironRainTheme.chrome.dimFg}>{prov.description}</text>
              </box>
              <Show when={prov.configured && prov.apiKey}>
                <box flexDirection="row" gap={1} paddingX={4}>
                  <text fg={ironRainTheme.chrome.dimFg}>key:</text>
                  <text fg={ironRainTheme.status.success}>
                    {prov.apiKey!.startsWith('env:') ? prov.apiKey! : `${prov.apiKey!.slice(0, 8)}...`}
                  </text>
                </box>
              </Show>
              <Show when={isActive() && props.editingKey}>
                <box flexDirection="row" gap={1} paddingX={4}>
                  <text fg={ironRainTheme.brand.primary}>API key:</text>
                  <text fg={props.keyBuffer ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                    {props.keyBuffer || 'type key or env:VAR_NAME'}
                  </text>
                  <text fg={ironRainTheme.chrome.muted}>_</text>
                </box>
                <box paddingX={4}>
                  <text fg={ironRainTheme.chrome.dimFg}>
                    [Enter] save · [Esc] cancel · Tip: use env:ANTHROPIC_API_KEY
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
