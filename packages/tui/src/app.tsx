import { createSignal, Show } from 'solid-js';
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

export function App(props: AppProps) {
  const [showSplash, setShowSplash] = createSignal(true);
  const hasConfig = () => !!props.config?.slots || !!findConfigFile();
  const [onboardingComplete, setOnboardingComplete] = createSignal(false);
  const [activeConfig, setActiveConfig] = createSignal<IronRainConfig | undefined>(props.config);

  // Auto-dismiss splash after 2 seconds
  setTimeout(() => setShowSplash(false), 2000);

  function handleOnboardingComplete(configPath: string) {
    // Reload the config that was just written
    const newConfig = loadConfig();
    setActiveConfig(newConfig);
    setOnboardingComplete(true);
  }

  function handleOnboardingQuit() {
    process.exit(0);
  }

  const needsOnboarding = () => !hasConfig() && !onboardingComplete();

  return (
    <SlateProvider config={activeConfig()}>
      <box flexDirection="column" width="100%" height="100%">
        <Show when={needsOnboarding()} fallback={
          <Show when={showSplash()} fallback={<SessionRoute />}>
            <SplashScreen version={props.version ?? '0.1.0'} />
          </Show>
        }>
          <OnboardingWizard
            onComplete={handleOnboardingComplete}
            onQuit={handleOnboardingQuit}
          />
        </Show>

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
