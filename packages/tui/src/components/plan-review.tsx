import type { Plan } from "@howlerops/iron-rain";
import { For } from "solid-js";
import { ironRainTheme } from "../theme/theme.js";

export interface PlanReviewProps {
  plan: Plan;
  onApprove: () => void;
  onReject: () => void;
  onEdit: (feedback: string) => void;
}

export function PlanReview(props: PlanReviewProps) {
  return (
    <box flexDirection="column" paddingX={1}>
      <text fg={ironRainTheme.brand.primary}>
        {`Plan Review: ${props.plan.title}`}
      </text>

      <box marginTop={1}>
        <text fg={ironRainTheme.chrome.fg}>{props.plan.prd}</text>
      </box>

      <box flexDirection="column" marginTop={1}>
        <text fg={ironRainTheme.brand.accent}>
          {`Tasks (${props.plan.tasks.length}):`}
        </text>
        <For each={props.plan.tasks}>
          {(task) => (
            <box flexDirection="column" marginLeft={2}>
              <text fg={ironRainTheme.chrome.fg}>
                {`${task.index + 1}. ${task.title}`}
              </text>
              <text fg={ironRainTheme.chrome.dimFg}>
                {`   ${task.description}`}
              </text>
            </box>
          )}
        </For>
      </box>

      <box flexDirection="row" marginTop={1} gap={1}>
        <text fg={ironRainTheme.status.success}>
          <b>[a]</b>
        </text>
        <text fg={ironRainTheme.status.success}>approve</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.status.error}>
          <b>[r]</b>
        </text>
        <text fg={ironRainTheme.status.error}>reject</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.status.warning}>
          <b>[e]</b>
        </text>
        <text fg={ironRainTheme.status.warning}>edit (type feedback)</text>
      </box>
    </box>
  );
}
