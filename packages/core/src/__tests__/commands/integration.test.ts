import { describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expandTemplate, loadCustomCommands } from "../../commands/loader.js";
import { buildSystemPrompt } from "../../orchestrator/prompts.js";

const TMP = join(tmpdir(), `iron-rain-test-cmd-int-${Date.now()}`);

function setup() {
  mkdirSync(TMP, { recursive: true });
}

function cleanup() {
  if (existsSync(TMP)) rmSync(TMP, { recursive: true });
}

describe("Custom command dispatch integration", () => {
  it("loads command and expands template for dispatch", () => {
    setup();
    try {
      const cmdDir = join(TMP, ".iron-rain", "commands");
      mkdirSync(cmdDir, { recursive: true });
      writeFileSync(
        join(cmdDir, "greet.md"),
        `---
name: greet
description: Say hello
---
Say hello to $ARGUMENTS`,
      );

      const commands = loadCustomCommands(TMP);
      expect(commands).toHaveLength(1);

      const cmd = commands[0];
      expect(cmd.name).toBe("/greet");

      // Simulate what handleSlashCommand does
      const expanded = expandTemplate(cmd.template, "world");
      expect(expanded).toBe("Say hello to world");
    } finally {
      cleanup();
    }
  });

  it("custom command with slot override preserves slot", () => {
    setup();
    try {
      const cmdDir = join(TMP, ".iron-rain", "commands");
      mkdirSync(cmdDir, { recursive: true });
      writeFileSync(
        join(cmdDir, "analyze.md"),
        `---
name: analyze
description: Analyze code
slot: explore
---
Analyze $ARGUMENTS in detail`,
      );

      const commands = loadCustomCommands(TMP);
      const cmd = commands[0];
      expect(cmd.slot).toBe("explore");
      expect(expandTemplate(cmd.template, "src/main.ts")).toBe(
        "Analyze src/main.ts in detail",
      );
    } finally {
      cleanup();
    }
  });

  it("multiple custom commands are all loadable", () => {
    setup();
    try {
      const cmdDir = join(TMP, ".iron-rain", "commands");
      mkdirSync(cmdDir, { recursive: true });
      writeFileSync(join(cmdDir, "cmd-a.md"), "Do A with $ARGUMENTS");
      writeFileSync(join(cmdDir, "cmd-b.md"), "Do B with $ARGUMENTS");
      writeFileSync(join(cmdDir, "cmd-c.md"), "Do C");

      const commands = loadCustomCommands(TMP);
      expect(commands).toHaveLength(3);
      const names = commands.map((c) => c.name).sort();
      expect(names).toEqual(["/cmd-a", "/cmd-b", "/cmd-c"]);
    } finally {
      cleanup();
    }
  });

  it("custom commands can be merged into slash menu entries", () => {
    setup();
    try {
      const cmdDir = join(TMP, ".iron-rain", "commands");
      mkdirSync(cmdDir, { recursive: true });
      writeFileSync(
        join(cmdDir, "deploy.md"),
        `---
name: deploy
description: Deploy app
---
Deploy $ARGUMENTS`,
      );

      const commands = loadCustomCommands(TMP);
      // Simulate the slash menu merge from slate-context.tsx
      const slashMenuEntries = commands.map((c) => ({
        name: c.name,
        description: c.description,
      }));

      expect(slashMenuEntries).toEqual([
        { name: "/deploy", description: "Deploy app" },
      ]);
    } finally {
      cleanup();
    }
  });
});

describe("systemPromptOverride integration", () => {
  it("override prepended to system prompt produces valid prompt", () => {
    const override =
      "You are a code review assistant. Focus on security issues.";
    const basePrompt = buildSystemPrompt("main");

    // Simulate what DispatchController.buildTask does
    const systemParts: string[] = [];
    systemParts.push(override, "---");
    systemParts.push(basePrompt);
    const combined = systemParts.join("\n\n");

    expect(combined).toContain(override);
    expect(combined).toContain("---");
    expect(combined).toContain("Cortex"); // from buildSystemPrompt("main")
    // Override should come first
    expect(combined.indexOf(override)).toBeLessThan(combined.indexOf("Cortex"));
  });

  it("no override produces normal system prompt", () => {
    const basePrompt = buildSystemPrompt("main");

    // Without override, systemParts has only the base prompt
    const systemParts: string[] = [];
    systemParts.push(basePrompt);
    const combined = systemParts.join("\n\n");

    expect(combined).not.toContain("---\n\n");
    expect(combined).toContain("Cortex");
  });
});
