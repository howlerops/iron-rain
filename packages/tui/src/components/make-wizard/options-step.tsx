import { ironRainTheme } from "../../theme/theme.js";
import type { MakeWizardOptions } from "./types.js";

export interface OptionsStepProps {
  options: MakeWizardOptions;
  cursor: number;
  editingNotes: boolean;
  notesEditValue: string;
}

const OPTION_LABELS = [
  "Auto-commit after each task",
  "Max iterations",
  "Use iterative loop (vs plan executor)",
  "Notes",
];

export function OptionsStep(props: OptionsStepProps) {
  const checkmark = (val: boolean) => (val ? "[x]" : "[ ]");

  return (
    <box flexDirection="column" paddingX={2} gap={0}>
      <text fg={ironRainTheme.brand.accent}>
        <b>Options</b>
      </text>

      {OPTION_LABELS.map((label, i) => {
        const isSelected = () => props.cursor === i;
        const color = () =>
          isSelected() ? ironRainTheme.brand.primary : ironRainTheme.chrome.fg;

        if (i === 0) {
          return (
            <text fg={color()}>
              {`${isSelected() ? "\u25B8" : " "} ${checkmark(props.options.autoCommit)} ${label}`}
            </text>
          );
        }
        if (i === 1) {
          return (
            <text fg={color()}>
              {`${isSelected() ? "\u25B8" : " "} ${label}: ${props.options.maxIterations}`}
              {isSelected() ? "  ←/→ to adjust" : ""}
            </text>
          );
        }
        if (i === 2) {
          return (
            <text fg={color()}>
              {`${isSelected() ? "\u25B8" : " "} ${checkmark(props.options.useLoop)} ${label}`}
            </text>
          );
        }
        // Notes
        if (props.editingNotes) {
          return (
            <box flexDirection="column">
              <text fg={color()}>
                {`${isSelected() ? "\u25B8" : " "} ${label}:`}
              </text>
              <text fg={ironRainTheme.chrome.fg}>
                {`   ${props.notesEditValue}\u2588`}
              </text>
              <box flexDirection="row" gap={1}>
                <text fg={ironRainTheme.chrome.muted}>
                  <b>[Esc]</b>
                </text>
                <text fg={ironRainTheme.chrome.dimFg}>done</text>
              </box>
            </box>
          );
        }
        return (
          <text fg={color()}>
            {`${isSelected() ? "\u25B8" : " "} ${label}: ${props.options.notes || "(empty)"}`}
          </text>
        );
      })}

      <box flexDirection="row" marginTop={1} gap={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[↑↓]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>navigate</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Space]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>toggle</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[←→]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>adjust</text>
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
