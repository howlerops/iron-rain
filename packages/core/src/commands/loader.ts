/**
 * Custom slash command loader.
 * Loads command templates from .iron-rain/commands/, .claude/commands/, etc.
 */

import { readFileSync } from "node:fs";
import { getResourcePaths, scanDirectory } from "../discovery.js";

export interface CustomCommand {
  name: string;
  description: string;
  slot?: string;
  template: string;
  source: string;
}

/**
 * Parse YAML-like frontmatter from a markdown command file.
 */
function parseFrontmatter(content: string): {
  meta: Record<string, string>;
  body: string;
} {
  const match = content.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);
  if (!match) return { meta: {}, body: content };

  const meta: Record<string, string> = {};
  for (const line of match[1].split("\n")) {
    const colonIdx = line.indexOf(":");
    if (colonIdx > 0) {
      const key = line.slice(0, colonIdx).trim();
      const value = line
        .slice(colonIdx + 1)
        .trim()
        .replace(/^["']|["']$/g, "");
      meta[key] = value;
    }
  }

  return { meta, body: match[2].trim() };
}

/**
 * Load custom commands from all discovery paths.
 * Scans .iron-rain/commands/, .claude/commands/, .cursor/commands/, .windsurf/commands/.
 */
export function loadCustomCommands(cwd: string): CustomCommand[] {
  const commands: CustomCommand[] = [];
  const paths = getResourcePaths("commands", cwd);

  for (const { path: dirPath } of paths) {
    const entries = scanDirectory(dirPath);
    for (const entry of entries) {
      try {
        const content = readFileSync(entry.filePath, "utf-8");
        const { meta, body } = parseFrontmatter(content);

        const name = meta.name ?? entry.name.replace(/\.(md|mdc|mdx)$/, "");
        commands.push({
          name: name.startsWith("/") ? name : `/${name}`,
          description: meta.description ?? "",
          slot: meta.slot,
          template: body,
          source: entry.filePath,
        });
      } catch {
        // Skip malformed command files
      }
    }
  }

  return commands;
}

/**
 * Expand a command template with user arguments.
 */
export function expandTemplate(template: string, args: string): string {
  return template.replace(/\$ARGUMENTS/g, args);
}
