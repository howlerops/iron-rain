import type { SlotAssignment, SlotName } from '@howlerops/iron-rain';
import { ironRainTheme, slotLabel } from '../theme/theme.js';

export interface ModelSlotBarProps {
  slots: SlotAssignment;
  activeSlot?: SlotName;
}

function SlotIndicator(props: { name: SlotName; model: string; active: boolean }) {
  const color = ironRainTheme.slots[props.name];
  const prefix = props.active ? '>' : ' ';
  return (
    <text color={color} bold={props.active}>
      {prefix} {slotLabel(props.name)}: {props.model}
    </text>
  );
}

export function ModelSlotBar(props: ModelSlotBarProps) {
  return (
    <box flexDirection="row" gap={2} paddingX={1}>
      <SlotIndicator
        name="main"
        model={props.slots.main.model}
        active={props.activeSlot === 'main'}
      />
      <text color={ironRainTheme.chrome.border}>|</text>
      <SlotIndicator
        name="explore"
        model={props.slots.explore.model}
        active={props.activeSlot === 'explore'}
      />
      <text color={ironRainTheme.chrome.border}>|</text>
      <SlotIndicator
        name="execute"
        model={props.slots.execute.model}
        active={props.activeSlot === 'execute'}
      />
    </box>
  );
}
