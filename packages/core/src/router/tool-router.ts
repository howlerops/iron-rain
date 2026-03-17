import type { SlotName, ToolType } from "../slots/types.js";

const TOOL_SLOT_MAP: Record<ToolType, SlotName> = {
  edit: "execute",
  write: "execute",
  bash: "execute",
  grep: "explore",
  glob: "explore",
  read: "explore",
  search: "explore",
  strategy: "main",
  plan: "main",
  conversation: "main",
};

export function getSlotForTool(toolType: ToolType): SlotName {
  return TOOL_SLOT_MAP[toolType];
}

export function getToolsForSlot(slot: SlotName): ToolType[] {
  return (Object.entries(TOOL_SLOT_MAP) as [ToolType, SlotName][])
    .filter(([, s]) => s === slot)
    .map(([t]) => t);
}
