import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";

const RULE_FILES = ["IRON-RAIN.md", "CLAUDE.md"];
const LOCAL_RULES_DIR = ".iron-rain/rules";
const GLOBAL_RULES_DIR = join(homedir(), ".iron-rain", "rules");

/**
 * Load project rules from standard locations.
 * Returns an array of rule strings (contents of each discovered file).
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

  // 2. Load .iron-rain/rules/*.md (local, sorted alphabetically)
  const localDir = resolve(cwd, LOCAL_RULES_DIR);
  rules.push(...loadRulesFromDir(localDir));

  // 3. Load ~/.iron-rain/rules/*.md (global)
  rules.push(...loadRulesFromDir(GLOBAL_RULES_DIR));

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

  const files = readdirSync(dir)
    .filter((f) => f.endsWith(".md"))
    .sort();

  const results: string[] = [];
  for (const file of files) {
    const content = safeReadFile(join(dir, file));
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
