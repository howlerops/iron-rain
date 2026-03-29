import type { Plan, SlotName } from "@howlerops/iron-rain";
import { For, Show } from "solid-js";
import { ironRainTheme } from "../theme/theme.js";
import type { ToolCallEntry } from "./session-view.js";
import {
  formatDuration,
  formatTokens,
  StreamingAgentCard,
} from "./session-view.js";

export interface PlanViewProps {
  plan: Plan;
  /** Whether a task dispatch is currently streaming */
  isLoading?: boolean;
  activeSlot?: SlotName;
  streamingContent?: string;
  streamingThinking?: string;
  streamingSystemPrompt?: string;
  streamingToolCalls?: ToolCallEntry[];
  streamingTask?: string;
  loadingStartTime?: number;
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
          {(task) => {
            const statParts = (): string => {
              if (!task.result) return "";
              const parts: string[] = [];
              if (task.result.duration > 0)
                parts.push(formatDuration(task.result.duration));
              if (task.result.filesModified.length > 0)
                parts.push(`${task.result.filesModified.length} files`);
              if (task.result.tokens > 0)
                parts.push(`${formatTokens(task.result.tokens)} tokens`);
              return parts.length > 0 ? ` (${parts.join(" \u00B7 ")})` : "";
            };
            return (
              <box flexDirection="column">
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
                  <Show when={task.result}>
                    <text fg={ironRainTheme.chrome.muted}>{statParts()}</text>
                  </Show>
                </box>

                {/* Show streaming activity card below the in-progress task */}
                <Show when={task.status === "in_progress" && props.isLoading}>
                  <box marginTop={0} marginBottom={1}>
                    <StreamingAgentCard
                      slot={props.activeSlot ?? "execute"}
                      task={props.streamingTask ?? task.title}
                      toolCalls={props.streamingToolCalls ?? []}
                      content={props.streamingContent ?? ""}
                      thinking={props.streamingThinking ?? ""}
                      systemPrompt={props.streamingSystemPrompt ?? ""}
                      startTime={props.loadingStartTime ?? Date.now()}
                    />
                  </box>
                </Show>
              </box>
            );
          }}
        </For>
      </box>

      <Show when={props.plan.stats.totalDuration > 0}>
        <box marginTop={1} flexDirection="column">
          {(() => {
            const totalFiles = props.plan.tasks.reduce(
              (sum, t) => sum + (t.result?.filesModified.length ?? 0),
              0,
            );
            const commits = props.plan.tasks
              .map((t) => t.result?.commitHash)
              .filter(Boolean);
            const parts = [
              formatDuration(props.plan.stats.totalDuration),
              `${formatTokens(props.plan.stats.totalTokens)} tokens`,
              `${totalFiles} files`,
            ];
            if (commits.length > 0) parts.push(`${commits.length} commits`);
            return (
              <text fg={ironRainTheme.chrome.dimFg}>
                {`Total: ${parts.join(" \u00B7 ")}`}
              </text>
            );
          })()}
        </box>
      </Show>
    </box>
  );
}
