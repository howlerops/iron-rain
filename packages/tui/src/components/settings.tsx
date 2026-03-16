import { createSignal } from 'solid-js';
import { createStore } from 'solid-js/store';
import { For } from 'solid-js';
import type { SlotName, SlotConfig, IronRainConfig } from '@howlerops/iron-rain';
import { SLOT_NAMES, writeConfig, loadConfig } from '@howlerops/iron-rain';
import { ironRainTheme, slotColor, slotLabel } from '../theme/theme.js';
import { PROVIDER_MODELS } from './onboarding/types.js';

export type SettingsSection = 'slots' | 'providers' | 'permissions';

export interface SettingsProps {
  config: IronRainConfig;
  onSave: (config: IronRainConfig) => void;
  onClose: () => void;
}

export function Settings(props: SettingsProps) {
  const [activeSection, setActiveSection] = createSignal<SettingsSection>('slots');
  const [sectionCursor, setSectionCursor] = createSignal(0);
  const [slotCursor, setSlotCursor] = createSignal(0);
  const [editing, setEditing] = createSignal(false);
  const [modelCursor, setModelCursor] = createSignal(0);

  const [config, setConfig] = createStore<IronRainConfig>(
    JSON.parse(JSON.stringify(props.config)),
  );

  const sections: SettingsSection[] = ['slots', 'providers', 'permissions'];
  const slotNames = [...SLOT_NAMES] as SlotName[];

  const providerIds = () => Object.keys(config.providers ?? {});

  const modelOptions = () => {
    const options: Array<{ provider: string; model: string }> = [];
    const providers = config.providers ?? {};
    for (const pid of Object.keys(providers)) {
      const models = PROVIDER_MODELS[pid] ?? [];
      for (const m of models) {
        options.push({ provider: pid, model: m });
      }
    }
    // Also include models from slots that might not be in providers
    if (config.slots) {
      for (const slot of slotNames) {
        const sc = config.slots[slot];
        if (sc) {
          const models = PROVIDER_MODELS[sc.provider] ?? [];
          for (const m of models) {
            if (!options.some(o => o.provider === sc.provider && o.model === m)) {
              options.push({ provider: sc.provider, model: m });
            }
          }
        }
      }
    }
    return options;
  };

  function handleSave() {
    const configPath = writeConfig(config);
    props.onSave(config);
  }

  return (
    <box flexDirection="column" paddingX={4} paddingY={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Settings</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Edit your Iron Rain configuration.
      </text>

      <box marginY={1} />

      {/* Section tabs */}
      <box flexDirection="row" gap={2} paddingX={1}>
        {sections.map((s) => {
          const isActive = () => s === activeSection();
          return (
            <text fg={isActive() ? ironRainTheme.brand.primary : ironRainTheme.chrome.muted}>
              {isActive() ? <b>{`[${s.toUpperCase()}]`}</b> : s}
            </text>
          );
        })}
      </box>

      <box marginY={1} />

      {/* Slots section */}
      {activeSection() === 'slots' && (
        <box flexDirection="column" border borderStyle="rounded" borderColor={ironRainTheme.chrome.border}
          paddingX={1} paddingY={1}>
          <text fg={ironRainTheme.brand.lightGold}><b>Model Slots</b></text>
          <box marginY={0} />
          <For each={slotNames}>
            {(slot, i) => {
              const isActive = () => i() === slotCursor();
              const slotConfig = () => config.slots?.[slot];
              return (
                <box flexDirection="row" gap={1} paddingX={1}>
                  <text fg={isActive() ? slotColor(slot) : ironRainTheme.chrome.dimFg}>
                    {isActive() ? '>' : ' '}
                  </text>
                  <text fg={slotColor(slot)}>
                    <b>{slotLabel(slot).padEnd(8)}</b>
                  </text>
                  <text fg={ironRainTheme.chrome.fg}>
                    {slotConfig()?.provider ?? '?'}/{slotConfig()?.model ?? '?'}
                  </text>
                </box>
              );
            }}
          </For>

          {editing() && (
            <box flexDirection="column" marginY={1} border
              borderStyle="rounded"
              borderColor={slotColor(slotNames[slotCursor()]!)} paddingX={1}>
              <text fg={slotColor(slotNames[slotCursor()]!)}>
                <b>Select model for {slotLabel(slotNames[slotCursor()]!)}:</b>
              </text>
              <For each={modelOptions()}>
                {(option, i) => {
                  const isSelected = () => i() === modelCursor();
                  return (
                    <box flexDirection="row" gap={1}>
                      <text fg={isSelected() ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                        {isSelected() ? '>' : ' '}
                      </text>
                      <text fg={isSelected() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                        {isSelected() ? <b>{option.provider}/{option.model}</b> : <>{option.provider}/{option.model}</>}
                      </text>
                    </box>
                  );
                }}
              </For>
            </box>
          )}
        </box>
      )}

      {/* Providers section */}
      {activeSection() === 'providers' && (
        <box flexDirection="column" border borderStyle="rounded" borderColor={ironRainTheme.chrome.border}
          paddingX={1} paddingY={1}>
          <text fg={ironRainTheme.brand.lightGold}><b>Configured Providers</b></text>
          <box marginY={0} />
          {providerIds().length === 0 ? (
            <text fg={ironRainTheme.chrome.muted} paddingX={1}>
              No providers configured. Run onboarding to set up providers.
            </text>
          ) : (
            <For each={providerIds()}>
              {(pid) => {
                const prov = () => (config.providers ?? {})[pid];
                return (
                  <box flexDirection="column" paddingX={1}>
                    <text fg={ironRainTheme.chrome.fg}><b>{pid}</b></text>
                    {prov()?.apiKey && (
                      <box flexDirection="row" gap={1} paddingX={2}>
                        <text fg={ironRainTheme.chrome.muted}>key:</text>
                        <text fg={ironRainTheme.status.success}>
                          {prov()!.apiKey!.startsWith('env:') ? prov()!.apiKey! : '***'}
                        </text>
                      </box>
                    )}
                    {prov()?.apiBase && (
                      <box flexDirection="row" gap={1} paddingX={2}>
                        <text fg={ironRainTheme.chrome.muted}>base:</text>
                        <text fg={ironRainTheme.status.success}>{prov()!.apiBase}</text>
                      </box>
                    )}
                  </box>
                );
              }}
            </For>
          )}
        </box>
      )}

      {/* Permissions section */}
      {activeSection() === 'permissions' && (
        <box flexDirection="column" border borderStyle="rounded" borderColor={ironRainTheme.chrome.border}
          paddingX={1} paddingY={1}>
          <text fg={ironRainTheme.brand.lightGold}><b>Permissions</b></text>
          <box marginY={0} />
          {Object.keys(config.permission ?? {}).length === 0 ? (
            <text fg={ironRainTheme.chrome.muted} paddingX={1}>
              Using default permissions (ask for all tools).
            </text>
          ) : (
            <For each={Object.entries(config.permission ?? {})}>
              {([tool, perm]) => (
                <box flexDirection="row" gap={1} paddingX={1}>
                  <text fg={ironRainTheme.chrome.fg}>{tool.padEnd(12)}</text>
                  <text fg={
                    perm === 'allow' ? ironRainTheme.status.success
                    : perm === 'deny' ? ironRainTheme.status.error
                    : ironRainTheme.status.warning
                  }>
                    {perm}
                  </text>
                </box>
              )}
            </For>
          )}
        </box>
      )}

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text fg={ironRainTheme.brand.primary}>
          <b>[Tab] Switch Section</b>
        </text>
        <text fg={ironRainTheme.brand.primary}>
          <b>[Enter] Edit</b>
        </text>
        <text fg={ironRainTheme.status.success}>
          <b>[s] Save</b>
        </text>
        <text fg={ironRainTheme.chrome.muted}>
          [Esc] Close
        </text>
      </box>
    </box>
  );
}
