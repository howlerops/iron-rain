import type { Plan, PlanTask } from "@howlerops/iron-rain";
import { For, Show } from "solid-js";
import { ironRainTheme } from "../theme/theme.js";

export interface PlanViewProps {
  plan: Plan;
  streamingContent?: string;
}

function taskStatusIcon(status: string): string {
  switch (status) {
    case "completed":
      return "\u2713"; // checkmark
    case "in_progress":
      return "\u25B8"; // arrow
    case "failed":
      return "\u2717"; // x
    case "skipped":
      return "-";
    default:
      return "\u25CB"; // circle
  }
}

function taskStatusColor(status: string): string {
  switch (status) {
    case "completed":
      return ironRainTheme.status.success;
    case "in_progress":
      return ironRainTheme.brand.primary;
    case "failed":
      return ironRainTheme.status.error;
    default:
      return ironRainTheme.chrome.dimFg;
  }
}

export function PlanView(props: PlanViewProps) {
  return (
    <box flexDirection="column" paddingX={1}>
      <text fg={ironRainTheme.brand.primary}>
        {`Plan: ${props.plan.title}`}
      </text>
      <text fg={ironRainTheme.chrome.dimFg}>
        {`Status: ${props.plan.status} | Tasks: ${props.plan.stats.tasksCompleted}/${props.plan.tasks.length}`}
      </text>

      <box flexDirection="column" marginTop={1}>
        <For each={props.plan.tasks}>
          {(task) => (
            <box flexDirection="row" gap={1}>
              <text fg={taskStatusColor(task.status)}>
                {`${taskStatusIcon(task.status)}`}
              </text>
              <text
                fg={
                  task.status === "in_progress"
                    ? ironRainTheme.chrome.fg
                    : ironRainTheme.chrome.dimFg
                }
              >
                {`${task.index + 1}. ${task.title}`}
              </text>
              <Show when={task.result?.duration}>
                <text fg={ironRainTheme.chrome.muted}>
                  {`(${Math.round((task.result?.duration ?? 0) / 1000)}s)`}
                </text>
              </Show>
            </box>
          )}
        </For>
      </box>

      <Show when={props.streamingContent}>
        <box marginTop={1} paddingX={1}>
          <text fg={ironRainTheme.chrome.fg}>{props.streamingContent}</text>
        </box>
      </Show>

      <Show when={props.plan.stats.totalDuration > 0}>
        <box marginTop={1}>
          <text fg={ironRainTheme.chrome.dimFg}>
            {`Total: ${Math.round(props.plan.stats.totalDuration / 1000)}s | ${props.plan.stats.totalTokens} tokens`}
          </text>
        </box>
      </Show>
    </box>
  );
}
