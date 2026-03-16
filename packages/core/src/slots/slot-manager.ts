import type { SlotName, SlotConfig, SlotAssignment, ToolType } from './types.js';
import { DEFAULT_SLOT_ASSIGNMENT } from './defaults.js';
import { getSlotForTool } from '../router/tool-router.js';

export class ModelSlotManager {
  private slots: SlotAssignment;

  constructor(config?: Partial<SlotAssignment>) {
    this.slots = { ...DEFAULT_SLOT_ASSIGNMENT, ...config };
  }

  getSlot(name: SlotName): SlotConfig {
    return this.slots[name];
  }

  setSlot(name: SlotName, config: SlotConfig): void {
    this.slots[name] = config;
  }

  cycleSlot(name: SlotName, available: SlotConfig[]): SlotConfig {
    if (available.length === 0) return this.slots[name];

    const current = this.slots[name];
    const currentIdx = available.findIndex(
      (c) => c.provider === current.provider && c.model === current.model,
    );
    const nextIdx = (currentIdx + 1) % available.length;
    this.slots[name] = available[nextIdx];
    return this.slots[name];
  }

  getSlotForTool(toolType: ToolType): SlotName {
    return getSlotForTool(toolType);
  }

  serialize(): SlotAssignment {
    return { ...this.slots };
  }

  getAllSlots(): SlotAssignment {
    return { ...this.slots };
  }
}
