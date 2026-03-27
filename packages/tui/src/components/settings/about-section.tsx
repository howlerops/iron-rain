import { ironRainTheme } from "../../theme/theme.js";

export function AboutSection() {
  return (
    <box flexDirection="column" paddingX={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Iron Rain</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>
        Multi-model orchestration for terminal-based coding
      </text>

      <box marginY={1} />
      <text fg={ironRainTheme.chrome.fg}>
        <b>Paths</b>
      </text>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.dimFg}>{"Config:  "}</text>
        <text fg={ironRainTheme.chrome.fg}>.iron-rain/config.json</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.dimFg}>{"Data:    "}</text>
        <text fg={ironRainTheme.chrome.fg}>~/.iron-rain/sessions.db</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.dimFg}>{"Plugins: "}</text>
        <text fg={ironRainTheme.chrome.fg}>.iron-rain/plugins/</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.dimFg}>{"Commands:"}</text>
        <text fg={ironRainTheme.chrome.fg}>.iron-rain/commands/</text>
      </box>

      <box marginY={1} />
      <text fg={ironRainTheme.chrome.fg}>
        <b>Keyboard Shortcuts</b>
      </text>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.muted}>{"Ctrl+S  "}</text>
        <text fg={ironRainTheme.chrome.dimFg}>Open settings</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.muted}>{"Ctrl+N  "}</text>
        <text fg={ironRainTheme.chrome.dimFg}>New session</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.muted}>{"Ctrl+L  "}</text>
        <text fg={ironRainTheme.chrome.dimFg}>Clear screen</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.muted}>{"Tab     "}</text>
        <text fg={ironRainTheme.chrome.dimFg}>Cycle focus / autocomplete</text>
      </box>
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.muted}>{"Esc     "}</text>
        <text fg={ironRainTheme.chrome.dimFg}>Cancel / close overlay</text>
      </box>

      <box marginY={1} />
      <text fg={ironRainTheme.chrome.dimFg}>
        github.com/howlerops/iron-rain
      </text>
    </box>
  );
}
