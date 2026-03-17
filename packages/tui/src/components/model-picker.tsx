import type { SlotName } from '@howlerops/iron-rain';
import { ironRainTheme, slotLabel } from '../theme/theme.js';

export interface ModelOption {
  provider: string;
  model: string;
}

export interface ModelPickerProps {
  slot: SlotName;
  options: ModelOption[];
  selectedIndex: number;
  onSelect: (option: ModelOption) => void;
}

export function ModelPicker(props: ModelPickerProps) {
  const color = ironRainTheme.slots[props.slot];

  return (
    <box flexDirection="column" border borderStyle="rounded" borderColor={color} paddingX={1}>
      <text fg={color}>
        <b>Select model for {slotLabel(props.slot)}</b>
      </text>
      {props.options.map((opt, i) => (
        <text fg={i === props.selectedIndex ? color : ironRainTheme.chrome.fg}>
          {i === props.selectedIndex
            ? <b>{`> ${opt.provider}/${opt.model}`}</b>
            : `  ${opt.provider}/${opt.model}`}
        </text>
      ))}
    </box>
  );
}
