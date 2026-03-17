import { describe, expect, test } from "bun:test";

import { detectToolType } from "../../router/heuristics.js";

describe("detectToolType", () => {
  test("detects search prompts", () => {
    expect(detectToolType("find where is the auth middleware")).toBe("search");
  });

  test("detects read prompts", () => {
    expect(detectToolType("read the file content and inspect it")).toBe("read");
  });

  test("detects glob prompts", () => {
    expect(detectToolType("list directory structure and project layout")).toBe(
      "glob",
    );
  });

  test("detects edit prompts", () => {
    expect(detectToolType("edit and update this method")).toBe("edit");
  });

  test("detects write prompts", () => {
    expect(
      detectToolType("create a new file and implement feature logic"),
    ).toBe("write");
  });

  test("detects bash prompts", () => {
    expect(detectToolType("run test command in terminal shell")).toBe("bash");
  });

  test("detects strategy prompts", () => {
    expect(detectToolType("explain architecture design trade-off")).toBe(
      "strategy",
    );
  });

  test("detects plan prompts", () => {
    expect(detectToolType("plan roadmap and break down the work")).toBe("plan");
  });

  test("returns null when there is no strong signal", () => {
    expect(detectToolType("hello there")).toBeNull();
  });

  test("returns null when best score is below threshold", () => {
    expect(detectToolType("run this now")).toBeNull();
  });

  test("returns tool when score reaches threshold", () => {
    expect(detectToolType("run test in terminal shell command")).toBe("bash");
  });
});
