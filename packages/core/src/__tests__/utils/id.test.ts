import { describe, expect, test } from "bun:test";

import { generateId } from "../../utils/id.js";

describe("generateId", () => {
  test("returns a UUID v4 formatted id", () => {
    const id = generateId();
    expect(id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
  });

  test("generates unique values across many calls", () => {
    const ids = Array.from({ length: 1000 }, () => generateId());
    expect(new Set(ids).size).toBe(ids.length);
  });
});
