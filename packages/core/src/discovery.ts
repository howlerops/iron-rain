/**
 * Shared multi-source discovery paths.
 *
 * All resource loaders (skills, commands, rules, plugins) use this to find
 * files across native .iron-rain/ directories AND well-known AI tool locations
 * (Claude Code, Cursor, Windsurf).
 *
 * Priority: iron-rain (project) > iron-rain (user) > Claude Code > Cursor > Windsurf > Custom
 */

import { existsSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { extname, join } from "node:path";

export interface DiscoverySource {
  path: string;
  label: string;
}

/** Markdown-like extensions used for skills, commands, and rules. */
const MD_EXTENSIONS = new Set([".md", ".mdc", ".mdx"]);

export function isMarkdownFile(filename: string): boolean {
  return MD_EXTENSIONS.has(extname(filename).toLowerCase());
}

/**
 * Build discovery paths for a given resource subdirectory.
 *
 * @param subdir - e.g. "skills", "commands", "rules"
 * @param cwd    - project root (defaults to process.cwd())
 * @returns Ordered list of directories to scan, with labels for UI grouping.
 *
 * @example
 *   getResourcePaths("skills")
 *   // → .iron-rain/skills, ~/.iron-rain/skills,
 *   //   .claude/skills, ~/.claude/skills,
 *   //   .claude/commands (for "commands" subdir),
 *   //   .cursor/rules (for "rules" subdir), etc.
 */
export function getResourcePaths(
  subdir: string,
  cwd?: string,
): DiscoverySource[] {
  const home = homedir();
  const dir = cwd ?? process.cwd();

  const paths: DiscoverySource[] = [
    // Native iron-rain
    { path: join(dir, ".iron-rain", subdir), label: "Project" },
    { path: join(home, ".iron-rain", subdir), label: "User" },

    // Claude Code
    { path: join(dir, ".claude", subdir), label: "Claude Code" },
    { path: join(home, ".claude", subdir), label: "Claude Code (User)" },

    // Cursor
    { path: join(dir, ".cursor", subdir), label: "Cursor" },

    // Windsurf
    { path: join(dir, ".windsurf", subdir), label: "Windsurf" },
  ];

  return paths;
}

/**
 * Scan a directory for markdown files (flat) and subdirectory SKILL.md files.
 * Returns {name, filePath, parentDir?} tuples.
 */
export function scanDirectory(dirPath: string): Array<{
  name: string;
  filePath: string;
  parentDir?: string;
}> {
  if (!existsSync(dirPath)) return [];

  const results: Array<{
    name: string;
    filePath: string;
    parentDir?: string;
  }> = [];

  try {
    const entries = readdirSync(dirPath, { withFileTypes: true });
    for (const entry of entries) {
      if (entry.isFile() && isMarkdownFile(entry.name)) {
        results.push({
          name: entry.name,
          filePath: join(dirPath, entry.name),
        });
        continue;
      }

      // Subdirectory with SKILL.md (Claude Code skill convention)
      if (entry.isDirectory()) {
        const skillMd = join(dirPath, entry.name, "SKILL.md");
        if (existsSync(skillMd)) {
          results.push({
            name: entry.name,
            filePath: skillMd,
            parentDir: join(dirPath, entry.name),
          });
        }
      }
    }
  } catch {
    // Skip inaccessible directories
  }

  return results;
}
