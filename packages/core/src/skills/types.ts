/**
 * Skills system types — compatible with Claude Code skill format.
 */

export interface Skill {
  name: string;
  description: string;
  command?: string; // e.g. "/my-skill"
  content: string; // Markdown body (instructions)
  source: string; // File path or discovery source
  metadata?: Record<string, unknown>;
}

export interface SkillDiscoveryPath {
  path: string;
  label: string; // e.g. "Project", "User", "Claude Code"
}
