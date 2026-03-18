import { existsSync, readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { getResourcePaths, scanDirectory } from "../discovery.js";

const RULE_FILES = ["IRON-RAIN.md", "CLAUDE.md"];

/**
 * Load project rules from standard locations.
 * Returns an array of rule strings (contents of each discovered file).
 *
 * Sources (in order):
 *   1. IRON-RAIN.md or CLAUDE.md at project root
 *   2. .iron-rain/rules/, ~/.iron-rain/rules/
 *   3. .claude/rules/, ~/.claude/rules/
 *   4. .cursor/rules/, .windsurf/rules/
 */
export function loadProjectRules(cwd: string): string[] {
  const rules: string[] = [];

  // 1. Check for IRON-RAIN.md or CLAUDE.md at project root
  for (const name of RULE_FILES) {
    const path = resolve(cwd, name);
    if (existsSync(path)) {
      const content = safeReadFile(path);
      if (content) rules.push(content);
      break; // Use only the first match
    }
  }

  // 2. Load rules from all discovery paths
  const paths = getResourcePaths("rules", cwd);
  for (const { path: dirPath } of paths) {
    rules.push(...loadRulesFromDir(dirPath));
  }

  return rules;
}

function loadRulesFromDir(dir: string): string[] {
  if (!existsSync(dir)) return [];

  try {
    const stat = statSync(dir);
    if (!stat.isDirectory()) return [];
  } catch {
    return [];
  }

  const results: string[] = [];
  const entries = scanDirectory(dir);
  for (const entry of entries) {
    const content = safeReadFile(entry.filePath);
    if (content) results.push(content);
  }
  return results;
}

function safeReadFile(path: string): string | null {
  try {
    const content = readFileSync(path, "utf-8").trim();
    return content || null;
  } catch {
    return null;
  }
}
