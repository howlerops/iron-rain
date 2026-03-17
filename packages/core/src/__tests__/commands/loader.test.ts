import { describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { expandTemplate, loadCustomCommands } from "../../commands/loader.js";

const TMP = join(tmpdir(), "iron-rain-test-commands-" + Date.now());

function setup() {
  mkdirSync(TMP, { recursive: true });
}

function cleanup() {
  if (existsSync(TMP)) rmSync(TMP, { recursive: true });
}

describe("loadCustomCommands", () => {
  it("returns empty array when no commands directory exists", () => {
    setup();
    try {
      const commands = loadCustomCommands(TMP);
      expect(commands).toEqual([]);
    } finally {
      cleanup();
    }
  });

  it("loads commands from .iron-rain/commands/", () => {
    setup();
    try {
      const cmdDir = join(TMP, ".iron-rain", "commands");
      mkdirSync(cmdDir, { recursive: true });
      writeFileSync(
        join(cmdDir, "deploy.md"),
        `---
name: deploy
description: Deploy the application
slot: execute
---
Deploy the application with these arguments: $ARGUMENTS`,
      );

      const commands = loadCustomCommands(TMP);
      expect(commands.length).toBe(1);
      expect(commands[0].name).toBe("/deploy");
      expect(commands[0].description).toBe("Deploy the application");
      expect(commands[0].slot).toBe("execute");
      expect(commands[0].template).toContain("$ARGUMENTS");
    } finally {
      cleanup();
    }
  });

  it("uses filename as command name when not in frontmatter", () => {
    setup();
    try {
      const cmdDir = join(TMP, ".iron-rain", "commands");
      mkdirSync(cmdDir, { recursive: true });
      writeFileSync(join(cmdDir, "lint.md"), "Run linting on $ARGUMENTS");

      const commands = loadCustomCommands(TMP);
      expect(commands[0].name).toBe("/lint");
    } finally {
      cleanup();
    }
  });
});

describe("expandTemplate", () => {
  it("replaces $ARGUMENTS placeholder", () => {
    const result = expandTemplate("Review $ARGUMENTS for issues", "src/auth.ts");
    expect(result).toBe("Review src/auth.ts for issues");
  });

  it("handles multiple $ARGUMENTS placeholders", () => {
    const result = expandTemplate("File: $ARGUMENTS\nCheck: $ARGUMENTS", "test.ts");
    expect(result).toBe("File: test.ts\nCheck: test.ts");
  });

  it("handles empty arguments", () => {
    const result = expandTemplate("Do something with $ARGUMENTS", "");
    expect(result).toBe("Do something with ");
  });
});
