/**
 * Skill Registry — manages loaded skills and provides lookup.
 */

import { discoverSkills } from "./loader.js";
import type { Skill } from "./types.js";

export class SkillRegistry {
  private skills = new Map<string, Skill>();
  private byCommand = new Map<string, Skill>();

  register(skill: Skill): void {
    this.skills.set(skill.name, skill);
    if (skill.command) {
      this.byCommand.set(skill.command, skill);
    }
  }

  get(name: string): Skill | undefined {
    return this.skills.get(name);
  }

  getByCommand(command: string): Skill | undefined {
    return this.byCommand.get(command);
  }

  list(): Skill[] {
    return Array.from(this.skills.values());
  }

  /**
   * Scan discovery paths and register all found skills.
   */
  discover(extraPaths?: string[]): Skill[] {
    const discovered = discoverSkills(extraPaths);
    for (const skill of discovered) {
      this.register(skill);
    }
    return discovered;
  }

  /**
   * Get skills grouped by source label.
   */
  grouped(): Map<string, Skill[]> {
    const groups = new Map<string, Skill[]>();
    for (const skill of this.skills.values()) {
      const label = skill.source.split(":")[0] || "Unknown";
      const list = groups.get(label) ?? [];
      list.push(skill);
      groups.set(label, list);
    }
    return groups;
  }

  /**
   * Get all skill commands (for slash menu integration).
   */
  getCommands(): Array<{ name: string; description: string }> {
    return this.list()
      .filter((s) => s.command)
      .map((s) => ({ name: s.command!, description: s.description }));
  }

  clear(): void {
    this.skills.clear();
    this.byCommand.clear();
  }
}
