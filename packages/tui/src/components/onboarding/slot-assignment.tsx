import { For } from 'solid-js';
import type { SlotName, SlotConfig } from '@howlerops/iron-rain';
import { SLOT_NAMES } from '@howlerops/iron-rain';
import { ironRainTheme, slotColor, slotLabel } from '../../theme/theme.js';
import type { ProviderChoice } from './types.js';
import { PROVIDER_MODELS } from './types.js';

export interface SlotAssignmentProps {
  providers: ProviderChoice[];
  slots: Record<SlotName, SlotConfig>;
  activeSlot: SlotName;
  modelCursorIndex: number;
  onNext: () => void;
  onBack: () => void;
}

export function SlotAssignment(props: SlotAssignmentProps) {
  const selectedProviders = () => props.providers.filter(p => p.selected);

  const modelOptions = () => {
    const options: Array<{ provider: string; model: string }> = [];
    for (const p of selectedProviders()) {
      const models = PROVIDER_MODELS[p.id] ?? [];
      for (const m of models) {
        options.push({ provider: p.id, model: m });
      }
    }
    return options;
  };

  return (
    <box flexDirection="column" paddingX={4} paddingY={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Assign Models to Slots</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Choose a model for each slot. Use arrow keys to pick, Enter to confirm.
      </text>

      <box marginY={1} />

      {/* Current assignments */}
      <box flexDirection="row" gap={2} paddingX={1}>
        <For each={[...SLOT_NAMES]}>
          {(slot) => {
            const isActive = () => slot === props.activeSlot;
            const config = () => props.slots[slot];
            return (
              <box flexDirection="column" border
                borderStyle="rounded"
                borderColor={isActive() ? slotColor(slot) : ironRainTheme.chrome.border}
                paddingX={1} paddingY={0} minWidth={25}>
                <text fg={slotColor(slot)}>
                  <b>{isActive() ? '> ' : '  '}{slotLabel(slot)}</b>
                </text>
                <text fg={ironRainTheme.chrome.muted}>
                  {config().provider}/{config().model}
                </text>
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      {/* Model picker for active slot */}
      <box flexDirection="column" border
        borderStyle="rounded"
        borderColor={slotColor(props.activeSlot)}
        paddingX={1} paddingY={1}>
        <text fg={slotColor(props.activeSlot)}>
          <b>Select model for {slotLabel(props.activeSlot)} slot:</b>
        </text>
        <box marginY={0} />
        <For each={modelOptions()}>
          {(option, i) => {
            const isSelected = () => i() === props.modelCursorIndex;
            const isCurrent = () =>
              props.slots[props.activeSlot].provider === option.provider &&
              props.slots[props.activeSlot].model === option.model;
            return (
              <box flexDirection="row" gap={1}>
                <text fg={isSelected() ? slotColor(props.activeSlot) : ironRainTheme.chrome.dimFg}>
                  {isSelected() ? '>' : ' '}
                </text>
                <text fg={isSelected() ? ironRainTheme.chrome.fg : ironRainTheme.chrome.muted}>
                  {isSelected() ? <b>{option.provider}/{option.model}</b> : <>{option.provider}/{option.model}</>}
                </text>
                {isCurrent() && (
                  <text fg={ironRainTheme.status.success}>*</text>
                )}
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text fg={ironRainTheme.brand.primary}>
          <b>[Enter] Assign & Next Slot</b>
        </text>
        <text fg={ironRainTheme.chrome.muted}>
          [Backspace] Back
        </text>
      </box>
    </box>
  );
}
