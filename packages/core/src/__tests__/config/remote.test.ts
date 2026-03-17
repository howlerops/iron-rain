import { describe, expect, it } from "bun:test";
import { deepMergeConfig } from "../../config/remote.js";

describe("deepMergeConfig", () => {
  it("merges flat objects", () => {
    const remote = { a: 1, b: 2 };
    const local = { b: 3, c: 4 };
    const result = deepMergeConfig(remote, local);
    expect(result).toEqual({ a: 1, b: 3, c: 4 });
  });

  it("deep merges nested objects", () => {
    const remote = { nested: { a: 1, b: 2 } };
    const local = { nested: { b: 3, c: 4 } };
    const result = deepMergeConfig(remote, local);
    expect(result).toEqual({ nested: { a: 1, b: 3, c: 4 } });
  });

  it("local values win on conflict", () => {
    const remote = { key: "remote-value" };
    const local = { key: "local-value" };
    const result = deepMergeConfig(remote, local);
    expect(result.key).toBe("local-value");
  });

  it("arrays are replaced, not merged", () => {
    const remote = { arr: [1, 2, 3] };
    const local = { arr: [4, 5] };
    const result = deepMergeConfig(remote, local);
    expect(result.arr).toEqual([4, 5]);
  });

  it("handles empty objects", () => {
    expect(deepMergeConfig({}, {})).toEqual({});
    expect(deepMergeConfig({ a: 1 }, {})).toEqual({ a: 1 });
    expect(deepMergeConfig({}, { b: 2 })).toEqual({ b: 2 });
  });
});
