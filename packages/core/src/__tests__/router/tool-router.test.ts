import { describe, expect, test } from "bun:test";

import { getSlotForTool, getToolsForSlot } from "../../router/tool-router.js";
import type { SlotName, ToolType } from "../../slots/types.js";

describe("tool router", () => {
  test("maps each tool type to the expected slot", () => {
    const expectedMappings: Record<ToolType, SlotName> = {
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

    for (const [tool, slot] of Object.entries(expectedMappings) as [
      ToolType,
      SlotName,
    ][]) {
      expect(getSlotForTool(tool)).toBe(slot);
    }
  });

  test("getToolsForSlot returns execute tools", () => {
    expect(getToolsForSlot("execute")).toEqual(["edit", "write", "bash"]);
  });

  test("getToolsForSlot returns explore tools", () => {
    expect(getToolsForSlot("explore")).toEqual([
      "grep",
      "glob",
      "read",
      "search",
    ]);
  });

  test("getToolsForSlot returns main tools", () => {
    expect(getToolsForSlot("main")).toEqual([
      "strategy",
      "plan",
      "conversation",
    ]);
  });
});
