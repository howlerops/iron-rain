import { ironRainTheme } from '../theme/theme.js';

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
      <text color={ironRainTheme.brand.primary} bold>
        Sessions
      </text>
      <text color={ironRainTheme.chrome.muted} marginBottom={1}>
        [n] New session | [enter] Open | [q] Quit
      </text>

      {props.sessions.length === 0 ? (
        <text color={ironRainTheme.chrome.dimFg}>
          No sessions yet. Press [n] to start.
        </text>
      ) : (
        props.sessions.map((s) => (
          <box flexDirection="row" gap={2} paddingY={0}>
            <text color={ironRainTheme.chrome.fg} bold>
              {s.name}
            </text>
            <text color={ironRainTheme.chrome.dimFg}>
              {s.lastMessage.slice(0, 60)}
            </text>
          </box>
        ))
      )}
    </box>
  );
}
