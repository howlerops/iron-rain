import type { PlanTask } from "@howlerops/iron-rain";
import { For } from "solid-js";
import { ironRainTheme } from "../../theme/theme.js";

export interface TasksStepProps {
  tasks: PlanTask[];
  cursor: number;
  removedIndices: Set<number>;
}

const STATUS_ICONS: Record<string, string> = {
  pending: "\u25CB",
  in_progress: "\u25D4",
  completed: "\u25CF",
  failed: "\u2717",
  skipped: "\u2014",
};

export function TasksStep(props: TasksStepProps) {
  return (
    <box flexDirection="column" paddingX={2} gap={0}>
      <text fg={ironRainTheme.brand.accent}>
        <b>Tasks</b>
      </text>

      <For each={props.tasks}>
        {(task, i) => {
          const isSelected = () => i() === props.cursor;
          const isRemoved = () => props.removedIndices.has(i());
          const icon = STATUS_ICONS[task.status] ?? "\u25CB";
          const desc =
            task.description.length > 80
              ? `${task.description.slice(0, 80)}...`
              : task.description;

          return (
            <box flexDirection="column" marginLeft={1}>
              <text
                fg={
                  isRemoved()
                    ? ironRainTheme.chrome.dimFg
                    : isSelected()
                      ? ironRainTheme.brand.primary
                      : ironRainTheme.chrome.fg
                }
              >
                {`${isSelected() ? "\u25B8" : " "} ${isRemoved() ? "\u2717" : icon} ${task.index + 1}. ${isRemoved() ? `~~${task.title}~~` : task.title}`}
              </text>
              <text fg={ironRainTheme.chrome.dimFg}>{`     ${desc}`}</text>
            </box>
          );
        }}
      </For>

      <box flexDirection="row" marginTop={1} gap={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[↑↓]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>navigate</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[d]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>remove/restore</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Enter]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>continue</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Backspace]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>back</text>
      </box>
    </box>
  );
}
