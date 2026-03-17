import { createSignal, Switch, Match } from 'solid-js';
import { useKeyboard, useRenderer } from '@opentui/solid';
import type { IronRainConfig } from '@howlerops/iron-rain';
import { loadConfig, checkForUpdate } from '@howlerops/iron-rain';
import { SlateProvider, useSlate } from './context/slate-context.js';
import { SessionRoute } from './routes/session.js';
import { OnboardingWizard } from './components/onboarding/index.js';
import { UpdateBanner } from './components/update-banner.js';

export interface AppProps {
  config?: IronRainConfig;
  version?: string;
}

type View = 'onboarding' | 'session';

/**
 * Check provider configs for issues (missing API keys, unreachable endpoints).
 * Returns a list of warning messages.
 */
function checkProviderHealth(config?: IronRainConfig): string[] {
  const warnings: string[] = [];
  if (!config?.slots) return warnings;

  const slots = config.slots;
  const providers = config.providers ?? {};

  for (const [slotName, slotConfig] of Object.entries(slots)) {
    if (!slotConfig) continue;
    const { provider, apiKey } = slotConfig;

    // Check if the provider needs an API key
    const needsKey = ['anthropic', 'openai', 'gemini'].includes(provider);
    if (!needsKey) continue;

    // Resolve key: check slot-level key, provider-level key, or env var
    const resolvedKey = apiKey
      ?? providers[provider]?.apiKey
      ?? undefined;

    if (!resolvedKey) {
      warnings.push(`**${slotName}** slot: No API key for ${provider}. Run /settings to configure.`);
      continue;
    }

    // If key is an env: reference, check the env var exists
    if (resolvedKey.startsWith('env:')) {
      const envVar = resolvedKey.slice(4);
      if (!process.env[envVar]) {
        warnings.push(`**${slotName}** slot: Environment variable \`${envVar}\` is not set.`);
      }
    }
  }

  return warnings;
}

function AppContent(props: { initialView: View; version: string; config?: IronRainConfig }) {
  const [, actions] = useSlate();
  const renderer = useRenderer();
  const [view, setView] = createSignal<View>(props.initialView);
  const [updateInfo, setUpdateInfo] = createSignal<{ current: string; latest: string } | null>(null);

  function quit() {
    renderer?.destroy();
    process.exit(0);
  }

  // Run provider health check on session start
  if (props.initialView === 'session') {
    setTimeout(() => {
      const warnings = checkProviderHealth(props.config);
      if (warnings.length > 0) {
        actions.addMessage({
          id: `health-${Date.now()}`,
          role: 'assistant',
          content: `**Provider Issues Detected**\n\n${warnings.join('\n')}\n\nType **/settings** to fix provider configuration.`,
          slot: 'main',
          timestamp: Date.now(),
        });
      }
    }, 100);

    // Background update check (non-blocking, 5s timeout)
    const autoCheck = props.config?.updates?.autoCheck !== false;
    if (autoCheck) {
      checkForUpdate(props.version).then((result) => {
        if (result.updateAvailable) {
          setUpdateInfo({ current: result.currentVersion, latest: result.latestVersion });
        }
      }).catch(() => {
        // Silently fail — update check is best-effort
      });
    }
  }

  function handleOnboardingComplete(_configPath: string) {
    const newConfig = loadConfig();
    if (newConfig.slots) {
      actions.updateSlots(newConfig.slots);
    }
    setView('session');
  }

  function handleOnboardingQuit() {
    quit();
  }

  // Global Ctrl+C handler
  useKeyboard((e) => {
    if (e.ctrl && e.name === 'c') {
      quit();
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
        <Match when={view() === 'session'}>
          <SessionRoute version={props.version} onQuit={quit} />
          {updateInfo() && (
            <UpdateBanner
              currentVersion={updateInfo()!.current}
              latestVersion={updateInfo()!.latest}
              visible={true}
              onDismiss={() => setUpdateInfo(null)}
            />
          )}
        </Match>
      </Switch>
    </box>
  );
}

export function App(props: AppProps) {
  const hasValidConfig = !!(props.config?.slots && Object.keys(props.config.slots).length > 0);
  const initialView: View = hasValidConfig ? 'session' : 'onboarding';
  const version = props.version ?? '0.1.0';

  return (
    <SlateProvider config={props.config}>
      <AppContent initialView={initialView} version={version} config={props.config} />
    </SlateProvider>
  );
}
