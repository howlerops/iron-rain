import { ironRainTheme } from "../theme/theme.js";

export interface WelcomeScreenProps {
  model: string;
}

export function WelcomeScreen(props: WelcomeScreenProps) {
  return (
    <box
      flexDirection="column"
      alignItems="center"
      justifyContent="center"
      flexGrow={1}
      paddingTop={2}
    >
      <ascii_font
        text="Iron Rain"
        font="slick"
        color={[
          ironRainTheme.brand.darkGold,
          ironRainTheme.brand.primary,
          ironRainTheme.brand.accent,
        ]}
      />
      <text fg={ironRainTheme.chrome.muted}>
        Multi-model orchestration for terminal-based coding
      </text>

      <box flexDirection="column" alignItems="center" marginTop={2}>
        <text fg={ironRainTheme.chrome.fg}>
          Type a message below to get started
        </text>
        <text fg={ironRainTheme.chrome.dimFg} marginTop={1}>
          {`Using ${props.model}`}
        </text>
      </box>

      <box flexDirection="column" alignItems="center" marginTop={2}>
        <text fg={ironRainTheme.chrome.dimFg}>
          Try: "Explain this codebase" · "Fix the failing tests" · "Refactor
          auth module"
        </text>
        <text fg={ironRainTheme.chrome.dimFg} marginTop={1}>
          {`Type /help for commands`}
        </text>
      </box>
    </box>
  );
}
