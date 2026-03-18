/**
 * Skill Loader — parses skill markdown files with YAML frontmatter.
 */
import { readFileSync } from "node:fs";
import { basename, extname } from "node:path";
import {
  type DiscoverySource,
  getResourcePaths,
  scanDirectory,
} from "../discovery.js";
import type { Skill, SkillDiscoveryPath } from "./types.js";

/**
 * Parse YAML frontmatter from a markdown string.
 * Simple parser — handles key: value pairs, no nested objects.
 */
function parseFrontmatter(content: string): {
  metadata: Record<string, string>;
  body: string;
} {
  const match = content.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);
  if (!match) return { metadata: {}, body: content };

  const metadata: Record<string, string> = {};
  for (const line of match[1].split("\n")) {
    const colonIdx = line.indexOf(":");
    if (colonIdx === -1) continue;
    const key = line.slice(0, colonIdx).trim();
    const value = line.slice(colonIdx + 1).trim();
    if (key && value) metadata[key] = value;
  }

  return { metadata, body: match[2].trim() };
}

/**
 * Load a single skill from a file path.
 * For subdirectory skills ({dir}/{name}/SKILL.md), derives the name from the
 * parent directory rather than the filename.
 */
export function loadSkillFile(
  filePath: string,
  parentDir?: string,
): Skill | null {
  try {
    const raw = readFileSync(filePath, "utf-8");
    const { metadata, body } = parseFrontmatter(raw);

    const baseName = basename(filePath, extname(filePath));
    // For SKILL.md inside a named subdirectory, use the directory name
    const inferredName =
      baseName === "SKILL" && parentDir ? basename(parentDir) : baseName;
    const name = metadata.name || inferredName;
    const description = metadata.description || "";
    const command = metadata.command || `/${name}`;

    return {
      name,
      description,
      command: command.startsWith("/") ? command : `/${command}`,
      content: body,
      source: filePath,
      metadata,
    };
  } catch {
    return null;
  }
}

/**
 * Get default discovery paths for skills.
 * Uses shared multi-source discovery (iron-rain, Claude Code, Cursor, Windsurf).
 */
export function getDiscoveryPaths(): SkillDiscoveryPath[] {
  return getResourcePaths("skills");
}

/**
 * Discover all skills from default paths.
 * Supports:
 *   - Flat markdown files:    {dir}/my-skill.md
 *   - Subdirectory skills:    {dir}/my-skill/SKILL.md
 *   - Cursor .mdc files:      {dir}/my-rule.mdc
 */
export function discoverSkills(extraPaths?: string[]): Skill[] {
  const paths: DiscoverySource[] = getDiscoveryPaths();
  if (extraPaths) {
    for (const p of extraPaths) {
      paths.push({ path: p, label: "Custom" });
    }
  }

  const skills: Skill[] = [];
  const seen = new Set<string>();

  for (const { path: dirPath, label } of paths) {
    const entries = scanDirectory(dirPath);
    for (const entry of entries) {
      const skill = loadSkillFile(entry.filePath, entry.parentDir);
      if (skill && skill.command && !seen.has(skill.command)) {
        skill.source = `${label}: ${entry.filePath}`;
        skills.push(skill);
        seen.add(skill.command);
      }
    }
  }

  return skills;
}
