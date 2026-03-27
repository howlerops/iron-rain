import { ironRainTheme } from "../theme/theme.js";

export interface Session {
  id: string;
  name: string;
  lastMessage: string;
  timestamp: number;
}

export interface HomeRouteProps {
  sessions: Session[];
  onSelectSession: (id: string) => void;
  onNewSession: () => void;
}

export function HomeRoute(props: HomeRouteProps) {
  return (
    <box flexDirection="column" paddingX={2}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Sessions</b>
      </text>
      <box flexDirection="row" gap={1} marginBottom={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[n]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>new session</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[Enter]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>open</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>[q]</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>quit</text>
      </box>

      {props.sessions.length === 0 ? (
        <text fg={ironRainTheme.chrome.dimFg}>
          No sessions yet. Press [n] to start.
        </text>
      ) : (
        props.sessions.map((s) => (
          <box flexDirection="row" gap={2} paddingY={0}>
            <text fg={ironRainTheme.chrome.fg}>
              <b>{s.name}</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>
              {s.lastMessage.slice(0, 60)}
            </text>
          </box>
        ))
      )}
    </box>
  );
}
