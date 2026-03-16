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
    <box flexDirection="column" borderStyle="round" borderColor={color} paddingX={1}>
      <text color={color} bold>
        Select model for {slotLabel(props.slot)}
      </text>
      {props.options.map((opt, i) => (
        <text
          color={i === props.selectedIndex ? color : ironRainTheme.chrome.fg}
          bold={i === props.selectedIndex}
        >
          {i === props.selectedIndex ? '> ' : '  '}
          {opt.provider}/{opt.model}
        </text>
      ))}
    </box>
  );
}
