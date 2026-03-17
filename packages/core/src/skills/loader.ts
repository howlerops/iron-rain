/**
 * Skill Loader — parses skill markdown files with YAML frontmatter.
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Skill, SkillDiscoveryPath } from './types.js';

/**
 * Parse YAML frontmatter from a markdown string.
 * Simple parser — handles key: value pairs, no nested objects.
 */
function parseFrontmatter(content: string): { metadata: Record<string, string>; body: string } {
  const match = content.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);
  if (!match) return { metadata: {}, body: content };

  const metadata: Record<string, string> = {};
  for (const line of match[1].split('\n')) {
    const colonIdx = line.indexOf(':');
    if (colonIdx === -1) continue;
    const key = line.slice(0, colonIdx).trim();
    const value = line.slice(colonIdx + 1).trim();
    if (key && value) metadata[key] = value;
  }

  return { metadata, body: match[2].trim() };
}

/**
 * Load a single skill from a file path.
 */
export function loadSkillFile(filePath: string): Skill | null {
  try {
    const raw = fs.readFileSync(filePath, 'utf-8');
    const { metadata, body } = parseFrontmatter(raw);

    const name = metadata.name || path.basename(filePath, path.extname(filePath));
    const description = metadata.description || '';
    const command = metadata.command || `/${name}`;

    return {
      name,
      description,
      command: command.startsWith('/') ? command : `/${command}`,
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
 */
export function getDiscoveryPaths(): SkillDiscoveryPath[] {
  const home = process.env.HOME || process.env.USERPROFILE || '';
  const cwd = process.cwd();

  return [
    { path: path.join(cwd, '.iron-rain', 'skills'), label: 'Project' },
    { path: path.join(home, '.iron-rain', 'skills'), label: 'User' },
    { path: path.join(cwd, '.claude', 'skills'), label: 'Claude Code (Project)' },
    { path: path.join(home, '.claude', 'skills'), label: 'Claude Code (User)' },
  ];
}

/**
 * Discover all skills from default paths.
 */
export function discoverSkills(extraPaths?: string[]): Skill[] {
  const paths = getDiscoveryPaths();
  if (extraPaths) {
    for (const p of extraPaths) {
      paths.push({ path: p, label: 'Custom' });
    }
  }

  const skills: Skill[] = [];
  const seen = new Set<string>();

  for (const { path: dirPath, label } of paths) {
    if (!fs.existsSync(dirPath)) continue;

    try {
      const entries = fs.readdirSync(dirPath);
      for (const entry of entries) {
        if (!entry.endsWith('.md')) continue;
        const fullPath = path.join(dirPath, entry);
        const skill = loadSkillFile(fullPath);
        if (skill && !seen.has(skill.command!)) {
          skill.source = `${label}: ${fullPath}`;
          skills.push(skill);
          seen.add(skill.command!);
        }
      }
    } catch {
      // Skip inaccessible directories
    }
  }

  return skills;
}
