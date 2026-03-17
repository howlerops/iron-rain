import { Show, For } from 'solid-js';
import type { SlotName, SlotConfig, ThinkingLevel } from '@howlerops/iron-rain';
import { SLOT_NAMES } from '@howlerops/iron-rain';
import { ironRainTheme, slotColor, slotLabel } from '../../theme/theme.js';

export const THINKING_LEVELS: ThinkingLevel[] = ['off', 'low', 'medium', 'high'];

export interface ModelOption {
  provider: string;
  model: string;
}

export interface ModelsSectionProps {
  slots: Record<SlotName, SlotConfig> | undefined;
  cursor: number;
  editing: boolean;
  editCursor: number;
  editingThinking: boolean;
  thinkingCursor: number;
  modelOptions: ModelOption[];
  modelsLoading: boolean;
}

export function ModelsSection(props: ModelsSectionProps) {
  const slotNames = [...SLOT_NAMES] as SlotName[];

  return (
    <box flexDirection="column">
      <text fg={ironRainTheme.chrome.muted}>
        Assign a provider and model to each slot.
      </text>
      <box marginY={0} />
      <For each={slotNames}>
        {(slot, i) => {
          const isActive = () => i() === props.cursor;
          const sc = () => props.slots?.[slot];
          const thinking = () => sc()?.thinkingLevel ?? 'off';
          return (
            <box flexDirection="row" gap={1} paddingX={1}>
              <text fg={isActive() ? slotColor(slot) : ironRainTheme.chrome.dimFg}>
                {isActive() ? '\u25B8' : ' '}
              </text>
              <text fg={slotColor(slot)}>
                <b>{slotLabel(slot).padEnd(8)}</b>
              </text>
              <text fg={ironRainTheme.chrome.fg}>
                {`${sc()?.provider ?? '\u2014'}/${sc()?.model ?? '\u2014'}`}
              </text>
              <text fg={thinking() !== 'off' ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                {`[${thinking()}]`}
              </text>
            </box>
          );
        }}
      </For>
      <Show when={props.modelsLoading}>
        <box paddingX={2} marginTop={1}>
          <text fg={ironRainTheme.chrome.dimFg}>Loading models...</text>
        </box>
      </Show>

      <Show when={props.editing}>
        <box flexDirection="column" marginTop={1} paddingX={2}>
          <text fg={slotColor(slotNames[props.cursor]!)}>
            <b>{`Select model for ${slotLabel(slotNames[props.cursor]!)}:`}</b>
          </text>
          <For each={props.modelOptions}>
            {(option, i) => {
              const isSel = () => i() === props.editCursor;
              return (
                <box flexDirection="row" gap={1} paddingX={1}>
                  <text fg={isSel() ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                    {isSel() ? '\u25B8' : ' '}
                  </text>
                  <text fg={isSel() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                    {isSel() ? <b>{`${option.provider}/${option.model}`}</b> : `${option.provider}/${option.model}`}
                  </text>
                </box>
              );
            }}
          </For>
          <text fg={ironRainTheme.chrome.dimFg} marginTop={0}>
            [Enter] select · [Esc] cancel
          </text>
        </box>
      </Show>

      <Show when={props.editingThinking}>
        <box flexDirection="column" marginTop={1} paddingX={2}>
          <text fg={slotColor(slotNames[props.cursor]!)}>
            <b>{`Thinking level for ${slotLabel(slotNames[props.cursor]!)}:`}</b>
          </text>
          <For each={THINKING_LEVELS}>
            {(level, i) => {
              const isSel = () => i() === props.thinkingCursor;
              return (
                <box flexDirection="row" gap={1} paddingX={1}>
                  <text fg={isSel() ? ironRainTheme.brand.primary : ironRainTheme.chrome.dimFg}>
                    {isSel() ? '\u25B8' : ' '}
                  </text>
                  <text fg={isSel() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                    {isSel() ? <b>{level}</b> : level}
                  </text>
                </box>
              );
            }}
          </For>
          <text fg={ironRainTheme.chrome.dimFg} marginTop={0}>
            [Enter] select · [Esc] cancel
          </text>
        </box>
      </Show>
    </box>
  );
}
