import { ironRainTheme } from '../../theme/theme.js';

export function AboutSection() {
  return (
    <box flexDirection="column" paddingX={1}>
      <text fg={ironRainTheme.brand.primary}><b>Iron Rain</b></text>
      <text fg={ironRainTheme.chrome.muted}>
        Multi-model orchestration for terminal-based coding
      </text>
      <box marginY={1} />
      <box flexDirection="row" gap={1}>
        <text fg={ironRainTheme.chrome.dimFg}>Config:</text>
        <text fg={ironRainTheme.chrome.fg}>iron-rain.json</text>
      </box>
      <box flexDirection="row" gap={1}>
        <text fg={ironRainTheme.chrome.dimFg}>Data:</text>
        <text fg={ironRainTheme.chrome.fg}>~/.iron-rain/sessions.db</text>
      </box>
      <box marginY={1} />
      <text fg={ironRainTheme.chrome.dimFg}>
        github.com/howlerops/iron-rain
      </text>
    </box>
  );
}
