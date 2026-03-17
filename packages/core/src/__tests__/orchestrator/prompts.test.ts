import { describe, expect, it } from "bun:test";
import {
  buildEpisodeContext,
  buildSystemPrompt,
  structuredPrompt,
} from "../../orchestrator/prompts.js";

describe("buildSystemPrompt", () => {
  it("includes slot role", () => {
    const prompt = buildSystemPrompt("main");
    expect(prompt).toContain("Cortex");
  });

  it("includes custom prompt", () => {
    const prompt = buildSystemPrompt("main", "Custom instruction");
    expect(prompt).toContain("Custom instruction");
  });

  it("includes project rules when provided", () => {
    const prompt = buildSystemPrompt("main", undefined, {
      rules: ["Always use TypeScript", "Write tests for everything"],
    });
    expect(prompt).toContain("Project Rules");
    expect(prompt).toContain("Always use TypeScript");
    expect(prompt).toContain("Write tests for everything");
  });

  it("includes repo map when provided", () => {
    const prompt = buildSystemPrompt("main", undefined, {
      repoMap: "src/index.ts: main, App\nsrc/utils.ts: helper",
    });
    expect(prompt).toContain("Repository Map");
    expect(prompt).toContain("src/index.ts");
  });

  it("includes lessons when provided", () => {
    const prompt = buildSystemPrompt("main", undefined, {
      lessons: ["Use bun for testing", "Config is in iron-rain.json"],
    });
    expect(prompt).toContain("Lessons Learned");
    expect(prompt).toContain("Use bun for testing");
  });

  it("includes all context sections together", () => {
    const prompt = buildSystemPrompt("explore", "Extra info", {
      rules: ["Rule 1"],
      repoMap: "file map here",
      lessons: ["Lesson 1"],
    });
    expect(prompt).toContain("Scout");
    expect(prompt).toContain("Rule 1");
    expect(prompt).toContain("file map here");
    expect(prompt).toContain("Lesson 1");
    expect(prompt).toContain("Extra info");
  });
});

describe("buildEpisodeContext", () => {
  it("returns empty string for no episodes", () => {
    expect(buildEpisodeContext([])).toBe("");
  });

  it("formats episodes", () => {
    const episodes = [
      {
        id: "1",
        slot: "main" as const,
        task: "Fix bug",
        result: "Fixed the null pointer",
        tokens: 100,
        duration: 5000,
        status: "success" as const,
      },
    ];
    const result = buildEpisodeContext(episodes);
    expect(result).toContain("Fix bug");
    expect(result).toContain("success");
  });
});

describe("structuredPrompt", () => {
  it("includes goal section", () => {
    const prompt = structuredPrompt({ goal: "Build a feature" });
    expect(prompt).toContain("## GOAL");
    expect(prompt).toContain("Build a feature");
  });

  it("includes optional sections", () => {
    const prompt = structuredPrompt({
      goal: "Test",
      context: "Project context",
      output: "JSON format",
    });
    expect(prompt).toContain("## CONTEXT");
    expect(prompt).toContain("## OUTPUT FORMAT");
  });
});
