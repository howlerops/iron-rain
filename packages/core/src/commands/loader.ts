/**
 * Custom slash command loader.
 * Loads command templates from .iron-rain/commands/ directories.
 */

import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { homedir } from "node:os";

export interface CustomCommand {
  name: string;
  description: string;
  slot?: string;
  template: string;
  source: string;
}

const COMMANDS_DIR = ".iron-rain/commands";

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
      const value = line.slice(colonIdx + 1).trim().replace(/^["']|["']$/g, "");
      meta[key] = value;
    }
  }

  return { meta, body: match[2].trim() };
}

/**
 * Load custom commands from project and global directories.
 */
export function loadCustomCommands(cwd: string): CustomCommand[] {
  const commands: CustomCommand[] = [];
  const dirs = [
    resolve(cwd, COMMANDS_DIR),
    resolve(homedir(), COMMANDS_DIR),
  ];

  for (const dir of dirs) {
    if (!existsSync(dir)) continue;

    const files = readdirSync(dir).filter((f) => f.endsWith(".md"));
    for (const file of files) {
      try {
        const filePath = join(dir, file);
        const content = readFileSync(filePath, "utf-8");
        const { meta, body } = parseFrontmatter(content);

        const name = meta.name ?? file.replace(/\.md$/, "");
        commands.push({
          name: name.startsWith("/") ? name : `/${name}`,
          description: meta.description ?? "",
          slot: meta.slot,
          template: body,
          source: filePath,
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
