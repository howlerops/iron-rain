import { describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { loadProjectRules } from "../../rules/loader.js";

const TMP = join(tmpdir(), "iron-rain-test-rules-" + Date.now());

function setup() {
  mkdirSync(TMP, { recursive: true });
}

function cleanup() {
  if (existsSync(TMP)) rmSync(TMP, { recursive: true });
}

describe("loadProjectRules", () => {
  it("returns empty array when no rules files exist", () => {
    setup();
    try {
      const rules = loadProjectRules(TMP);
      expect(rules).toEqual([]);
    } finally {
      cleanup();
    }
  });

  it("loads IRON-RAIN.md from project root", () => {
    setup();
    try {
      writeFileSync(
        join(TMP, "IRON-RAIN.md"),
        "# Rule 1\nAlways use TypeScript",
      );
      const rules = loadProjectRules(TMP);
      expect(rules.length).toBe(1);
      expect(rules[0]).toContain("Always use TypeScript");
    } finally {
      cleanup();
    }
  });

  it("loads CLAUDE.md from project root", () => {
    setup();
    try {
      writeFileSync(join(TMP, "CLAUDE.md"), "# Claude Rule\nBe concise");
      const rules = loadProjectRules(TMP);
      expect(rules.length).toBe(1);
      expect(rules[0]).toContain("Be concise");
    } finally {
      cleanup();
    }
  });

  it("loads rules from .iron-rain/rules/ directory", () => {
    setup();
    try {
      const rulesDir = join(TMP, ".iron-rain", "rules");
      mkdirSync(rulesDir, { recursive: true });
      writeFileSync(join(rulesDir, "style.md"), "Use single quotes");
      writeFileSync(join(rulesDir, "testing.md"), "Always write tests");
      const rules = loadProjectRules(TMP);
      expect(rules.length).toBe(2);
    } finally {
      cleanup();
    }
  });

  it("combines multiple rule sources", () => {
    setup();
    try {
      writeFileSync(join(TMP, "IRON-RAIN.md"), "Root rule");
      const rulesDir = join(TMP, ".iron-rain", "rules");
      mkdirSync(rulesDir, { recursive: true });
      writeFileSync(join(rulesDir, "local.md"), "Local rule");
      const rules = loadProjectRules(TMP);
      expect(rules.length).toBe(2);
    } finally {
      cleanup();
    }
  });
});
