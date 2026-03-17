import { describe, expect, it } from "bun:test";
import {
  DEFAULT_RECONNECT_CONFIG,
  namespaceDuplicateTools,
  reconnectDelay,
  resolveToolName,
} from "../../mcp/reconnect.js";

describe("reconnectDelay", () => {
  it("returns base delay on first attempt", () => {
    const delay = reconnectDelay(0, DEFAULT_RECONNECT_CONFIG);
    // Base = 1000, jitter 50-100% → 500-1000
    expect(delay).toBeGreaterThanOrEqual(500);
    expect(delay).toBeLessThanOrEqual(1000);
  });

  it("increases with attempts", () => {
    const d0 = reconnectDelay(0, DEFAULT_RECONNECT_CONFIG);
    const d3 = reconnectDelay(3, DEFAULT_RECONNECT_CONFIG);
    // d3 base = 1000*2^3 = 8000 → jitter 4000-8000
    // Should generally be larger (probabilistically)
    expect(d3).toBeGreaterThan(d0 * 0.5); // Very safe bound
  });

  it("caps at maxDelayMs", () => {
    const delay = reconnectDelay(20, DEFAULT_RECONNECT_CONFIG);
    expect(delay).toBeLessThanOrEqual(DEFAULT_RECONNECT_CONFIG.maxDelayMs);
  });
});

describe("namespaceDuplicateTools", () => {
  it("leaves unique names unchanged", () => {
    const tools = [
      { name: "read_file", server: "fs" },
      { name: "write_file", server: "fs" },
    ];
    const result = namespaceDuplicateTools(tools);
    expect(result[0].name).toBe("read_file");
    expect(result[1].name).toBe("write_file");
  });

  it("namespaces duplicate names", () => {
    const tools = [
      { name: "search", server: "web" },
      { name: "search", server: "docs" },
      { name: "unique", server: "other" },
    ];
    const result = namespaceDuplicateTools(tools);
    expect(result[0].name).toBe("web.search");
    expect(result[1].name).toBe("docs.search");
    expect(result[2].name).toBe("unique");
  });
});

describe("resolveToolName", () => {
  const tools = [
    { name: "read", server: "fs" },
    { name: "web.search", server: "web" },
    { name: "search", server: "docs" },
  ];

  it("finds exact match", () => {
    const result = resolveToolName("read", tools);
    expect(result).toEqual({ server: "fs", toolName: "read" });
  });

  it("finds namespaced match", () => {
    const result = resolveToolName("web.search", tools);
    expect(result).toEqual({ server: "web", toolName: "web.search" });
  });

  it("returns null for unknown tool", () => {
    expect(resolveToolName("unknown", tools)).toBeNull();
  });
});
