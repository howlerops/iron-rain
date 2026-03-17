import { describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { loadIgnoreRules } from "../../context/ignore.js";

const TMP = join(tmpdir(), "iron-rain-test-ignore-" + Date.now());

function setup() {
  mkdirSync(TMP, { recursive: true });
}

function cleanup() {
  if (existsSync(TMP)) rmSync(TMP, { recursive: true });
}

describe("loadIgnoreRules", () => {
  it("returns filter that ignores nothing when no ignore file exists", () => {
    setup();
    try {
      const filter = loadIgnoreRules(TMP);
      expect(filter.isIgnored("src/index.ts")).toBe(false);
      expect(filter.isIgnored("node_modules/foo")).toBe(false);
    } finally {
      cleanup();
    }
  });

  it("loads .ironrainignore patterns", () => {
    setup();
    try {
      writeFileSync(
        join(TMP, ".ironrainignore"),
        "node_modules\n*.log\nbuild/",
      );
      const filter = loadIgnoreRules(TMP);
      expect(filter.isIgnored("node_modules/foo")).toBe(true);
      expect(filter.isIgnored("error.log")).toBe(true);
      expect(filter.isIgnored("build/output.js")).toBe(true);
      expect(filter.isIgnored("src/index.ts")).toBe(false);
    } finally {
      cleanup();
    }
  });

  it("falls back to .gitignore", () => {
    setup();
    try {
      writeFileSync(join(TMP, ".gitignore"), "dist/\n*.tmp");
      const filter = loadIgnoreRules(TMP);
      expect(filter.isIgnored("dist/bundle.js")).toBe(true);
      expect(filter.isIgnored("temp.tmp")).toBe(true);
      expect(filter.isIgnored("src/main.ts")).toBe(false);
    } finally {
      cleanup();
    }
  });

  it("handles comment lines and blank lines", () => {
    setup();
    try {
      writeFileSync(
        join(TMP, ".ironrainignore"),
        "# comment\n\nnode_modules\n# another comment",
      );
      const filter = loadIgnoreRules(TMP);
      expect(filter.isIgnored("node_modules/pkg")).toBe(true);
      expect(filter.isIgnored("src/file.ts")).toBe(false);
    } finally {
      cleanup();
    }
  });
});
