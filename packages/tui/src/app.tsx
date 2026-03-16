import { createSignal, Switch, Match } from 'solid-js';
import { useKeyboard, useRenderer } from '@opentui/solid';
import type { IronRainConfig } from '@howlerops/iron-rain';
import { loadConfig } from '@howlerops/iron-rain';
import { SlateProvider, useSlate } from './context/slate-context.js';
import { SplashScreen } from './components/splash-screen.js';
import { SessionRoute } from './routes/session.js';
import { OnboardingWizard } from './components/onboarding/index.js';

export interface AppProps {
  config?: IronRainConfig;
  version?: string;
}

type View = 'splash' | 'onboarding' | 'session';

function AppContent(props: { initialView: View; version: string }) {
  const [, actions] = useSlate();
  const renderer = useRenderer();
  const [view, setView] = createSignal<View>(props.initialView);

  function quit() {
    renderer?.destroy();
    process.exit(0);
  }

  // Auto-dismiss splash after 2 seconds
  if (props.initialView === 'splash') {
    setTimeout(() => setView('session'), 2000);
  }

  function handleOnboardingComplete(_configPath: string) {
    // Load the freshly written config and update slots in context
    const newConfig = loadConfig();
    if (newConfig.slots) {
      actions.updateSlots(newConfig.slots);
    }
    setView('session');
  }

  function handleOnboardingQuit() {
    quit();
  }

  // Global Ctrl+C handler + press any key to skip splash
  useKeyboard((e) => {
    if (e.ctrl && e.name === 'c') {
      quit();
    }
    if (view() === 'splash') {
      setView('session');
    }
  });

  return (
    <box flexDirection="column" width="100%" height="100%">
      <Switch>
        <Match when={view() === 'onboarding'}>
          <OnboardingWizard
            onComplete={handleOnboardingComplete}
            onQuit={handleOnboardingQuit}
          />
        </Match>
        <Match when={view() === 'splash'}>
          <SplashScreen version={props.version} />
        </Match>
        <Match when={view() === 'session'}>
          <SessionRoute version={props.version} onQuit={quit} />
        </Match>
      </Switch>
    </box>
  );
}

export function App(props: AppProps) {
  // Only skip onboarding if we have a config with actual slot assignments.
  // findConfigFile() walks up directories and may find unrelated config files,
  // so we check for real slot data, not just file existence.
  const hasValidConfig = !!(props.config?.slots && Object.keys(props.config.slots).length > 0);
  const initialView: View = hasValidConfig ? 'splash' : 'onboarding';
  const version = props.version ?? '0.1.0';

  return (
    <SlateProvider config={props.config}>
      <AppContent initialView={initialView} version={version} />
    </SlateProvider>
  );
}
