import type { SlotAssignment, SlotName } from "@howlerops/iron-rain";
import { ironRainTheme } from "../theme/theme.js";

export interface ModelSlotBarProps {
  slots: SlotAssignment;
  activeSlot?: SlotName;
}

export function ModelSlotBar(props: ModelSlotBarProps) {
  const model = props.slots.main.model;
  return (
    <box flexDirection="row" gap={1} paddingX={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Model:</b>
      </text>
      <text fg={ironRainTheme.chrome.fg}>{model}</text>
    </box>
  );
}
