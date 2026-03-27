import type { Plan } from "@howlerops/iron-rain";
import { ironRainTheme } from "../../theme/theme.js";
import type { MakeWizardOptions } from "./types.js";

export interface ConfirmStepProps {
  plan: Plan;
  options: MakeWizardOptions;
  removedCount: number;
  remainingCount: number;
}

export function ConfirmStep(props: ConfirmStepProps) {
  return (
    <box flexDirection="column" paddingX={2} gap={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Ready to execute</b>
      </text>

      <text fg={ironRainTheme.chrome.fg}>{`Plan: ${props.plan.title}`}</text>

      <text fg={ironRainTheme.chrome.fg}>
        {`Tasks: ${props.remainingCount} remaining${props.removedCount > 0 ? `, ${props.removedCount} removed` : ""}`}
      </text>

      <box flexDirection="column">
        <text fg={ironRainTheme.chrome.fg}>
          {`Auto-commit: ${props.options.autoCommit ? "yes" : "no"}`}
        </text>
        <text fg={ironRainTheme.chrome.fg}>
          {`Max iterations: ${props.options.maxIterations}`}
        </text>
        <text fg={ironRainTheme.chrome.fg}>
          {`Execution: ${props.options.useLoop ? "iterative loop" : "plan executor"}`}
        </text>
        {props.options.notes && (
          <text fg={ironRainTheme.chrome.fg}>
            {`Notes: ${props.options.notes}`}
          </text>
        )}
      </box>

      <box flexDirection="row" marginTop={1} gap={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Enter]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>execute</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Backspace]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>back</text>
      </box>
    </box>
  );
}
