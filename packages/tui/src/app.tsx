import { createSignal, Switch, Match } from 'solid-js';
import { useKeyboard } from '@opentui/solid';
import type { IronRainConfig } from '@howlerops/iron-rain';
import { findConfigFile, loadConfig } from '@howlerops/iron-rain';
import { SlateProvider } from './context/slate-context.js';
import { SplashScreen } from './components/splash-screen.js';
import { SessionRoute } from './routes/session.js';
import { OnboardingWizard } from './components/onboarding/index.js';
import { ironRainTheme } from './theme/theme.js';

export interface AppProps {
  config?: IronRainConfig;
  version?: string;
}

type View = 'splash' | 'onboarding' | 'session';

export function App(props: AppProps) {
  const hasConfig = !!props.config?.slots || !!findConfigFile();
  const initialView: View = hasConfig ? 'splash' : 'onboarding';
  const [view, setView] = createSignal<View>(initialView);
  const [activeConfig, setActiveConfig] = createSignal<IronRainConfig | undefined>(props.config);

  // Auto-dismiss splash after 2 seconds
  if (hasConfig) {
    setTimeout(() => setView('session'), 2000);
  }

  function handleOnboardingComplete(configPath: string) {
    const newConfig = loadConfig();
    setActiveConfig(newConfig);
    setView('session');
  }

  function handleOnboardingQuit() {
    process.exit(0);
  }

  // Global Ctrl+C handler + press any key to skip splash
  useKeyboard((e) => {
    if (e.ctrl && e.name === 'c') {
      process.exit(0);
    }
    if (view() === 'splash') {
      setView('session');
    }
  });

  return (
    <SlateProvider config={activeConfig()}>
      <box flexDirection="column" width="100%" height="100%">
        <Switch>
          <Match when={view() === 'onboarding'}>
            <OnboardingWizard
              onComplete={handleOnboardingComplete}
              onQuit={handleOnboardingQuit}
            />
          </Match>
          <Match when={view() === 'splash'}>
            <SplashScreen version={props.version ?? '0.1.0'} />
          </Match>
          <Match when={view() === 'session'}>
            <SessionRoute />
          </Match>
        </Switch>

        {/* Status bar */}
        <box
          flexDirection="row"
          justifyContent="space-between"
          paddingX={1}
          border
          borderStyle="rounded"
          borderColor={ironRainTheme.chrome.border}
        >
          <text fg={ironRainTheme.brand.primary}>
            <b>iron-rain</b>
          </text>
          <text fg={ironRainTheme.chrome.muted}>{props.version ?? '0.1.0'}</text>
        </box>
      </box>
    </SlateProvider>
  );
}
