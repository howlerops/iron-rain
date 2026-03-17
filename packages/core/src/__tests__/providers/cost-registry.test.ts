import { describe, expect, it } from "bun:test";
import { CostRegistry } from "../../providers/cost-registry.js";

describe("CostRegistry", () => {
  it("returns pricing for known models", () => {
    const registry = new CostRegistry();
    const pricing = registry.getPricing("claude-opus-4-6");
    expect(pricing).toBeTruthy();
    expect(pricing!.input).toBe(15);
    expect(pricing!.output).toBe(75);
  });

  it("returns null for unknown models", () => {
    const registry = new CostRegistry();
    expect(registry.getPricing("totally-unknown-model-xyz")).toBeNull();
  });

  it("supports partial model name matching", () => {
    const registry = new CostRegistry();
    const pricing = registry.getPricing("claude-sonnet-4-6");
    expect(pricing).toBeTruthy();
    expect(pricing!.input).toBe(3);
  });

  it("allows custom pricing that overrides defaults", () => {
    const registry = new CostRegistry();
    registry.setPrice("custom-model", { input: 1, output: 2 });
    const pricing = registry.getPricing("custom-model");
    expect(pricing).toEqual({ input: 1, output: 2 });
  });

  it("loads pricing from config", () => {
    const registry = new CostRegistry();
    registry.loadFromConfig({
      "my-model": { input: 5, output: 10 },
    });
    expect(registry.getPricing("my-model")).toEqual({ input: 5, output: 10 });
  });

  it("estimates cost correctly", () => {
    const registry = new CostRegistry();
    const cost = registry.estimateCost("gpt-4o", { input: 1000, output: 500 });
    expect(cost).toBeTruthy();
    // gpt-4o: $2.50/M input, $10/M output
    // 1000 input tokens = 0.0025, 500 output tokens = 0.005
    expect(cost).toBeCloseTo(0.0075, 4);
  });

  it("returns null cost for unknown models", () => {
    const registry = new CostRegistry();
    expect(registry.estimateCost("unknown", { input: 100, output: 100 })).toBeNull();
  });

  it("formats cost as dollar string", () => {
    expect(CostRegistry.formatCost(0.001)).toBe("$0.0010");
    expect(CostRegistry.formatCost(0.05)).toBe("$0.050");
    expect(CostRegistry.formatCost(1.5)).toBe("$1.50");
    expect(CostRegistry.formatCost(10.123)).toBe("$10.12");
  });
});
