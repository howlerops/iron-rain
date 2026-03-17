import { Show } from "solid-js";
import { ironRainTheme } from "../theme/theme.js";

export interface UpdateBannerProps {
  currentVersion: string;
  latestVersion: string;
  visible: boolean;
  onDismiss?: () => void;
}

export function UpdateBanner(props: UpdateBannerProps) {
  return (
    <Show when={props.visible}>
      <box flexDirection="row" paddingX={1} gap={1}>
        <text fg={ironRainTheme.status.warning}>
          {`Update available: ${props.currentVersion} -> ${props.latestVersion} — Run /update to install`}
        </text>
      </box>
    </Show>
  );
}
