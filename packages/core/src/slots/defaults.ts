import type { SlotAssignment } from "./types.js";

export const DEFAULT_SLOT_ASSIGNMENT: SlotAssignment = {
  main: {
    provider: "anthropic",
    model: "claude-sonnet-4-20250514",
  },
  explore: {
    provider: "anthropic",
    model: "claude-sonnet-4-20250514",
  },
  execute: {
    provider: "anthropic",
    model: "claude-sonnet-4-20250514",
  },
};
