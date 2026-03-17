import { For } from 'solid-js';
import { ironRainTheme } from '../../theme/theme.js';
import type { ProviderChoice } from './types.js';

export interface CredentialsProps {
  providers: ProviderChoice[];
  credentials: Record<string, { apiKey?: string; apiBase?: string }>;
  cursorIndex: number;
  editing: boolean;
  editValue: string;
  onNext: () => void;
  onBack: () => void;
}

export function Credentials(props: CredentialsProps) {
  const needsSetup = () => props.providers.filter(p => p.selected && (p.requiresKey || p.defaultApiBase));

  return (
    <box flexDirection="column" paddingX={4} paddingY={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Provider Credentials</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Enter API keys or connection details. Use "env:VAR_NAME" to reference environment variables.
      </text>

      <box marginY={1} />

      <box flexDirection="column" border borderStyle="rounded" borderColor={ironRainTheme.chrome.border}
        paddingX={1} paddingY={1}>
        <For each={needsSetup()}>
          {(provider, i) => {
            const isActive = () => i() === props.cursorIndex;
            const cred = () => props.credentials[provider.id] ?? {};
            const hasEnvKey = () => {
              if (provider.keyEnvVar && process.env[provider.keyEnvVar]) return true;
              return false;
            };

            return (
              <box flexDirection="column" marginY={0}>
                <box flexDirection="row" gap={1}>
                  <text fg={isActive() ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                    {isActive() ? '>' : ' '}
                  </text>
                  <text fg={isActive() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                    {isActive() ? <b>{provider.name}</b> : provider.name}
                  </text>
                  {provider.requiresKey && hasEnvKey() && (
                    <text fg={ironRainTheme.status.success}>
                      {`(found ${provider.keyEnvVar} in env)`}
                    </text>
                  )}
                </box>
                {provider.requiresKey && (
                  <box flexDirection="row" gap={1} paddingX={3}>
                    <text fg={ironRainTheme.chrome.muted}>API Key:</text>
                    {isActive() && props.editing ? (
                      <text fg={ironRainTheme.brand.primary}>
                        {`${props.editValue}_`}
                      </text>
                    ) : (
                      <text fg={cred().apiKey ? ironRainTheme.status.success : ironRainTheme.chrome.dimFg}>
                        {cred().apiKey
                          ? (cred().apiKey!.startsWith('env:') ? cred().apiKey! : '***' + cred().apiKey!.slice(-4))
                          : (hasEnvKey() ? `env:${provider.keyEnvVar}` : 'not set')}
                      </text>
                    )}
                  </box>
                )}
                {provider.defaultApiBase && (
                  <box flexDirection="row" gap={1} paddingX={3}>
                    <text fg={ironRainTheme.chrome.muted}>API Base:</text>
                    <text fg={ironRainTheme.status.success}>
                      {cred().apiBase ?? provider.defaultApiBase}
                    </text>
                  </box>
                )}
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text fg={ironRainTheme.brand.primary}>
          <b>[Enter] Edit/Confirm</b>
        </text>
        <text fg={ironRainTheme.brand.primary}>
          <b>[Tab] Skip (use env)</b>
        </text>
        <text fg={ironRainTheme.chrome.muted}>
          [Backspace] Back
        </text>
      </box>
    </box>
  );
}
