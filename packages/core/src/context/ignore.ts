import { existsSync, readFileSync } from "node:fs";
import { isAbsolute, join, relative } from "node:path";

const IGNORE_FILES = [".ironrainignore", ".gitignore"];

export interface IgnoreFilter {
  isIgnored(filePath: string): boolean;
}

/**
 * Load ignore rules from .ironrainignore (preferred) or .gitignore (fallback).
 */
export function loadIgnoreRules(cwd: string): IgnoreFilter {
  for (const name of IGNORE_FILES) {
    const path = join(cwd, name);
    if (existsSync(path)) {
      const content = safeReadFile(path);
      if (content) {
        return createIgnoreFilter(content, cwd);
      }
    }
  }
  return { isIgnored: () => false };
}

function createIgnoreFilter(content: string, cwd: string): IgnoreFilter {
  const patterns = content
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#"));

  const matchers = patterns.map(parsePattern);

  return {
    isIgnored(filePath: string): boolean {
      const rel = isAbsolute(filePath)
        ? relative(cwd, filePath).replace(/\\/g, "/")
        : filePath.replace(/\\/g, "/");
      if (!rel || rel.startsWith("..")) return false;
      return matchers.some((m) => m(rel));
    },
  };
}

function parsePattern(pattern: string): (path: string) => boolean {
  const negated = pattern.startsWith("!");
  const raw = negated ? pattern.slice(1) : pattern;

  const regex = globToRegex(raw);

  return (path: string) => {
    const matches = regex.test(path);
    return negated ? false : matches;
  };
}

function globToRegex(glob: string): RegExp {
  let pattern = glob;

  // Remove leading/trailing slashes for matching
  pattern = pattern.replace(/^\//, "").replace(/\/$/, "");

  // Escape regex special chars except * and ?
  pattern = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&");

  // Handle glob patterns
  pattern = pattern.replace(/\*\*/g, "{{GLOBSTAR}}");
  pattern = pattern.replace(/\*/g, "[^/]*");
  pattern = pattern.replace(/\?/g, "[^/]");
  pattern = pattern.replace(/\{\{GLOBSTAR\}\}/g, ".*");

  // If pattern doesn't contain a slash, match against filename component
  if (!glob.includes("/")) {
    return new RegExp(`(^|/)${pattern}(/|$)`);
  }

  return new RegExp(`^${pattern}(/|$)`);
}

function safeReadFile(path: string): string | null {
  try {
    const content = readFileSync(path, "utf-8").trim();
    return content || null;
  } catch {
    return null;
  }
}
