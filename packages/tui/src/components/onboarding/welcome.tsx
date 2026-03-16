import { ironRainTheme } from '../../theme/theme.js';

export interface WelcomeProps {
  onNext: () => void;
}

export function Welcome(props: WelcomeProps) {
  return (
    <box flexDirection="column" alignItems="center" paddingY={2} paddingX={4}>
      <text color={ironRainTheme.brand.primary} bold>
        Iron Rain
      </text>
      <text color={ironRainTheme.chrome.muted}>
        Multi-model orchestration for coding agents
      </text>

      <box marginY={1} />

      <box flexDirection="column" paddingX={2} paddingY={1}
        borderStyle="round" borderColor={ironRainTheme.chrome.border}>
        <text color={ironRainTheme.chrome.fg}>
          Welcome! Let's set up Iron Rain in a few quick steps:
        </text>
        <box marginY={0} />
        <text color={ironRainTheme.brand.lightGold}>
          1. Choose your model providers
        </text>
        <text color={ironRainTheme.brand.lightGold}>
          2. Enter API keys (if needed)
        </text>
        <text color={ironRainTheme.brand.lightGold}>
          3. Assign models to slots
        </text>
        <text color={ironRainTheme.brand.lightGold}>
          4. Save your configuration
        </text>
      </box>

      <box marginY={1} />

      <text color={ironRainTheme.chrome.muted}>
        This will create an iron-rain.json config file.
      </text>

      <box marginY={1} />

      <box flexDirection="row" gap={2}>
        <text color={ironRainTheme.brand.primary} bold>
          [Enter] Continue
        </text>
        <text color={ironRainTheme.chrome.muted}>
          [q] Quit
        </text>
      </box>
    </box>
  );
}
