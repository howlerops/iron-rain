import { describe, expect, it } from "bun:test";
import { shouldSummarize, truncateWithContext } from "../../context/summarizer.js";

describe("shouldSummarize", () => {
  it("returns false for short output", () => {
    expect(shouldSummarize("hello world")).toBe(false);
  });

  it("returns true for long output", () => {
    const long = "x".repeat(10000);
    expect(shouldSummarize(long)).toBe(true);
  });

  it("respects custom threshold", () => {
    expect(shouldSummarize("hello", 3)).toBe(true);
    expect(shouldSummarize("hello", 100)).toBe(false);
  });
});

describe("truncateWithContext", () => {
  it("returns short output unchanged", () => {
    expect(truncateWithContext("hello", 1000)).toBe("hello");
  });

  it("truncates long output keeping start and end", () => {
    const output = "START" + "x".repeat(10000) + "END";
    const result = truncateWithContext(output, 200);
    expect(result).toContain("START");
    expect(result).toContain("END");
    expect(result).toContain("[...truncated");
    expect(result.length).toBeLessThan(output.length);
  });

  it("includes truncated character count", () => {
    const output = "a".repeat(1000);
    const result = truncateWithContext(output, 200);
    expect(result).toMatch(/\[\.\.\.truncated \d+ characters\.\.\.\]/);
  });
});
