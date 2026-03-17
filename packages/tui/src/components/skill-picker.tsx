import type { Skill } from "@howlerops/iron-rain";
import { For } from "solid-js";
import { ironRainTheme } from "../theme/theme.js";

export interface SkillPickerProps {
  skills: Skill[];
  groupedBySource?: boolean;
}

export function SkillPicker(props: SkillPickerProps) {
  const grouped = () => {
    const groups = new Map<string, Skill[]>();
    for (const skill of props.skills) {
      const label = skill.source.split(":")[0] || "Unknown";
      const list = groups.get(label) ?? [];
      list.push(skill);
      groups.set(label, list);
    }
    return groups;
  };

  return (
    <box flexDirection="column" paddingX={1}>
      <text fg={ironRainTheme.brand.primary}>Available Skills</text>

      <For each={Array.from(grouped().entries())}>
        {([source, skills]) => (
          <box flexDirection="column" marginTop={1}>
            <text fg={ironRainTheme.brand.accent}>{source}</text>
            <For each={skills}>
              {(skill) => (
                <box flexDirection="row" gap={1} marginLeft={2}>
                  <text fg={ironRainTheme.chrome.fg}>
                    {skill.command ?? `/${skill.name}`}
                  </text>
                  <text fg={ironRainTheme.chrome.dimFg}>
                    {skill.description}
                  </text>
                </box>
              )}
            </For>
          </box>
        )}
      </For>

      {props.skills.length === 0 && (
        <text fg={ironRainTheme.chrome.dimFg}>
          No skills found. Add .md files to .iron-rain/skills/ or
          .claude/skills/
        </text>
      )}
    </box>
  );
}
