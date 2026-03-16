import { createSignal, Show } from 'solid-js';
import type { IronRainConfig } from '@iron-rain/core';
import { SlateProvider } from './context/slate-context.js';
import { SplashScreen } from './components/splash-screen.js';
import { SessionRoute } from './routes/session.js';
import { ironRainTheme } from './theme/theme.js';

export interface AppProps {
  config?: IronRainConfig;
  version?: string;
}

export function App(props: AppProps) {
  const [showSplash, setShowSplash] = createSignal(true);

  // Auto-dismiss splash after 2 seconds
  setTimeout(() => setShowSplash(false), 2000);

  return (
    <SlateProvider config={props.config}>
      <box flexDirection="column" width="100%" height="100%">
        <Show when={showSplash()} fallback={<SessionRoute />}>
          <SplashScreen version={props.version ?? '0.1.0'} />
        </Show>

        {/* Status bar */}
        <box
          flexDirection="row"
          justifyContent="space-between"
          paddingX={1}
          borderStyle="round"
          borderColor={ironRainTheme.chrome.border}
        >
          <text color={ironRainTheme.brand.primary} bold>
            iron-rain
          </text>
          <text color={ironRainTheme.chrome.muted}>{props.version ?? '0.1.0'}</text>
        </box>
      </box>
    </SlateProvider>
  );
}
