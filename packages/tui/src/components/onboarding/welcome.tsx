import { ironRainTheme } from "../../theme/theme.js";

export interface WelcomeProps {
  onNext: () => void;
}

export function Welcome(_props: WelcomeProps) {
  return (
    <box flexDirection="column" alignItems="center" paddingY={2} paddingX={4}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Iron Rain</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Multi-model orchestration for coding agents
      </text>

      <box marginY={1} />

      <box
        flexDirection="column"
        paddingX={2}
        paddingY={1}
        border
        borderStyle="rounded"
        borderColor={ironRainTheme.chrome.border}
      >
        <text fg={ironRainTheme.chrome.fg}>
          Welcome! Let's set up Iron Rain in a few quick steps:
        </text>
        <box marginY={0} />
        <text fg={ironRainTheme.brand.lightGold}>
          1. Choose your model providers
        </text>
        <text fg={ironRainTheme.brand.lightGold}>
          2. Enter API keys (if needed)
        </text>
        <text fg={ironRainTheme.brand.lightGold}>
          3. Assign models to slots
        </text>
        <text fg={ironRainTheme.brand.lightGold}>
          4. Save your configuration
        </text>
      </box>

      <box marginY={1} />

      <text fg={ironRainTheme.chrome.muted}>
        This will create an iron-rain.json config file.
      </text>

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text fg={ironRainTheme.brand.primary}>
          <b>[Enter] Continue</b>
        </text>
        <text fg={ironRainTheme.chrome.muted}>[q] Quit</text>
      </box>
    </box>
  );
}
