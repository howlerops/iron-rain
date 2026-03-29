import { describe, expect, it } from "bun:test";
import { buildSystemPrompt } from "../../orchestrator/prompts.js";

describe("buildSystemPrompt with quality gates", () => {
  it("includes quality gates in system prompt", () => {
    const prompt = buildSystemPrompt("main", undefined, {
      qualityGates: ["lint: bunx biome check .", "typecheck: tsc --noEmit"],
    });
    expect(prompt).toContain("## Quality Gates");
    expect(prompt).toContain("lint: bunx biome check .");
    expect(prompt).toContain("typecheck: tsc --noEmit");
    expect(prompt).toContain("MUST pass");
  });

  it("omits quality gates section when empty", () => {
    const prompt = buildSystemPrompt("main", undefined, {
      qualityGates: [],
    });
    expect(prompt).not.toContain("Quality Gates");
  });

  it("omits quality gates section when undefined", () => {
    const prompt = buildSystemPrompt("main", undefined, {});
    expect(prompt).not.toContain("Quality Gates");
  });

  it("includes quality gates alongside rules and repo map", () => {
    const prompt = buildSystemPrompt("main", undefined, {
      rules: ["Always use TypeScript"],
      repoMap: "src/index.ts",
      qualityGates: ["lint: eslint ."],
    });
    expect(prompt).toContain("## Project Rules");
    expect(prompt).toContain("## Repository Map");
    expect(prompt).toContain("## Quality Gates");
  });
});
