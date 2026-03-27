import { ironRainTheme } from "../theme/theme.js";

export interface PermissionDialogProps {
  tool: string;
  description: string;
  onAllow: () => void;
  onDeny: () => void;
}

export function PermissionDialog(props: PermissionDialogProps) {
  return (
    <box
      flexDirection="column"
      border
      borderStyle="rounded"
      borderColor={ironRainTheme.status.warning}
      paddingX={2}
      paddingY={1}
    >
      <text fg={ironRainTheme.status.warning}>
        <b>Permission Required</b>
      </text>
      <text fg={ironRainTheme.chrome.fg} marginTop={1}>
        Tool: {props.tool}
      </text>
      <text fg={ironRainTheme.chrome.muted}>{props.description}</text>
      <box flexDirection="row" gap={1} marginTop={1}>
        <text fg={ironRainTheme.status.success}>
          <b>[y]</b>
        </text>
        <text fg={ironRainTheme.status.success}>allow</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.status.error}>
          <b>[n]</b>
        </text>
        <text fg={ironRainTheme.status.error}>deny</text>
      </box>
    </box>
  );
}
