import { ironRainTheme } from '../theme/theme.js';

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
      borderStyle="round"
      borderColor={ironRainTheme.status.warning}
      paddingX={2}
      paddingY={1}
    >
      <text color={ironRainTheme.status.warning} bold>
        Permission Required
      </text>
      <text color={ironRainTheme.chrome.fg} marginTop={1}>
        Tool: {props.tool}
      </text>
      <text color={ironRainTheme.chrome.muted}>{props.description}</text>
      <box flexDirection="row" gap={2} marginTop={1}>
        <text color={ironRainTheme.status.success}>[y] Allow</text>
        <text color={ironRainTheme.status.error}>[n] Deny</text>
      </box>
    </box>
  );
}
