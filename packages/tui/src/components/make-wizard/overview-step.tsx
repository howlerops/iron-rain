import type { Plan } from "@howlerops/iron-rain";
import { ironRainTheme } from "../../theme/theme.js";

export interface OverviewStepProps {
  plan: Plan;
  editing: boolean;
  editValue: string;
}

export function OverviewStep(props: OverviewStepProps) {
  return (
    <box flexDirection="column" paddingX={2} gap={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>{props.plan.title}</b>
      </text>

      <text fg={ironRainTheme.chrome.fg}>{props.plan.description}</text>

      {props.plan.prd && (
        <box flexDirection="column" marginTop={1}>
          <text fg={ironRainTheme.brand.accent}>
            <b>PRD</b>
          </text>
          <text fg={ironRainTheme.chrome.fg}>{props.plan.prd}</text>
        </box>
      )}

      {props.editing ? (
        <box flexDirection="column" marginTop={1}>
          <text fg={ironRainTheme.brand.accent}>Feedback:</text>
          <text fg={ironRainTheme.chrome.fg}>{`${props.editValue}\u2588`}</text>
          <box flexDirection="row" gap={1}>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[Enter]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>regenerate</text>
            <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[Esc]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>cancel</text>
          </box>
        </box>
      ) : (
        <box flexDirection="row" marginTop={1} gap={1}>
          <text fg={ironRainTheme.chrome.muted}>
            <b>[Enter]</b>
          </text>
          <text fg={ironRainTheme.chrome.dimFg}>continue</text>
          <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
          <text fg={ironRainTheme.chrome.muted}>
            <b>[e]</b>
          </text>
          <text fg={ironRainTheme.chrome.dimFg}>edit</text>
          <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
          <text fg={ironRainTheme.chrome.muted}>
            <b>[Esc]</b>
          </text>
          <text fg={ironRainTheme.chrome.dimFg}>cancel</text>
        </box>
      )}
    </box>
  );
}
