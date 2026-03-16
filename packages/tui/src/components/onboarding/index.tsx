import { createSignal } from 'solid-js';
import { createStore } from 'solid-js/store';
import type { SlotName, SlotConfig } from '@howlerops/iron-rain';
import { SLOT_NAMES, writeConfig } from '@howlerops/iron-rain';
import { ironRainTheme } from '../../theme/theme.js';
import type { OnboardingStep, OnboardingState, ProviderChoice } from './types.js';
import { AVAILABLE_PROVIDERS, PROVIDER_MODELS } from './types.js';
import { Welcome } from './welcome.js';
import { ProviderSelect } from './provider-select.js';
import { Credentials } from './credentials.js';
import { SlotAssignment } from './slot-assignment.js';
import { Summary } from './summary.js';

export interface OnboardingWizardProps {
  onComplete: (configPath: string) => void;
  onQuit: () => void;
}

const STEPS: OnboardingStep[] = ['welcome', 'providers', 'credentials', 'slots', 'summary'];

function defaultSlots(): Record<SlotName, SlotConfig> {
  return {
    main: { provider: 'anthropic', model: 'claude-sonnet-4-20250514' },
    explore: { provider: 'anthropic', model: 'claude-sonnet-4-20250514' },
    execute: { provider: 'anthropic', model: 'claude-sonnet-4-20250514' },
  };
}

export function OnboardingWizard(props: OnboardingWizardProps) {
  const [state, setState] = createStore<OnboardingState>({
    step: 'welcome',
    providers: AVAILABLE_PROVIDERS.map(p => ({ ...p })),
    credentials: {},
    slots: defaultSlots(),
  });

  // Provider select state
  const [providerCursor, setProviderCursor] = createSignal(0);

  // Credentials state
  const [credCursor, setCredCursor] = createSignal(0);
  const [credEditing, setCredEditing] = createSignal(false);
  const [credEditValue, setCredEditValue] = createSignal('');

  // Slot assignment state
  const [activeSlotIndex, setActiveSlotIndex] = createSignal(0);
  const [modelCursor, setModelCursor] = createSignal(0);

  const slotNames = [...SLOT_NAMES] as SlotName[];

  const activeSlot = (): SlotName => slotNames[activeSlotIndex()]!;

  const selectedProviders = () => state.providers.filter(p => p.selected);

  const modelOptions = () => {
    const options: Array<{ provider: string; model: string }> = [];
    for (const p of selectedProviders()) {
      const models = PROVIDER_MODELS[p.id] ?? [];
      for (const m of models) {
        options.push({ provider: p.id, model: m });
      }
    }
    return options;
  };

  const needsCredentials = () =>
    state.providers.filter(p => p.selected && (p.requiresKey || p.defaultApiBase));

  function goToStep(step: OnboardingStep) {
    setState('step', step);
    // Reset cursors when entering a step
    if (step === 'providers') setProviderCursor(0);
    if (step === 'credentials') {
      setCredCursor(0);
      setCredEditing(false);
    }
    if (step === 'slots') {
      setActiveSlotIndex(0);
      setModelCursor(0);
    }
  }

  function nextStep() {
    const idx = STEPS.indexOf(state.step);
    if (idx < STEPS.length - 1) {
      const next = STEPS[idx + 1]!;
      // Skip credentials if no providers need setup
      if (next === 'credentials' && needsCredentials().length === 0) {
        goToStep('slots');
      } else {
        goToStep(next);
      }
    }
  }

  function prevStep() {
    const idx = STEPS.indexOf(state.step);
    if (idx > 0) {
      const prev = STEPS[idx - 1]!;
      // Skip credentials if no providers need setup
      if (prev === 'credentials' && needsCredentials().length === 0) {
        goToStep('providers');
      } else {
        goToStep(prev);
      }
    }
  }

  function toggleProvider(index: number) {
    setState('providers', index, 'selected', (v) => !v);
  }

  function setCredential(providerId: string, key: string, value: string) {
    setState('credentials', providerId, (prev) => ({ ...prev, [key]: value }));
  }

  function setSlotConfig(slot: SlotName, provider: string, model: string) {
    setState('slots', slot, { provider, model });
  }

  function handleSave() {
    // Build config from onboarding state
    const providers: Record<string, { apiKey?: string; apiBase?: string }> = {};
    for (const p of selectedProviders()) {
      const cred = state.credentials[p.id];
      const entry: { apiKey?: string; apiBase?: string } = {};
      if (cred?.apiKey) entry.apiKey = cred.apiKey;
      if (cred?.apiBase) {
        entry.apiBase = cred.apiBase;
      } else if (p.defaultApiBase) {
        entry.apiBase = p.defaultApiBase;
      }
      if (Object.keys(entry).length > 0) {
        providers[p.id] = entry;
      }
    }

    const configPath = writeConfig({
      slots: state.slots,
      providers,
    });

    props.onComplete(configPath);
  }

  // Keyboard handler — called by useInput in the parent
  function handleInput(input: string, key: { name?: string; ctrl?: boolean; shift?: boolean }) {
    const step = state.step;

    // Global quit
    if (input === 'q' && step === 'welcome') {
      props.onQuit();
      return;
    }

    switch (step) {
      case 'welcome': {
        if (key.name === 'return') nextStep();
        break;
      }

      case 'providers': {
        if (key.name === 'up') {
          setProviderCursor(c => Math.max(0, c - 1));
        } else if (key.name === 'down') {
          setProviderCursor(c => Math.min(state.providers.length - 1, c + 1));
        } else if (input === ' ') {
          toggleProvider(providerCursor());
        } else if (key.name === 'return') {
          if (selectedProviders().length > 0) {
            // Update default slots to use first selected provider's first model
            const first = selectedProviders()[0]!;
            const firstModel = PROVIDER_MODELS[first.id]?.[0] ?? '';
            for (const s of slotNames) {
              setSlotConfig(s, first.id, firstModel);
            }
            nextStep();
          }
        } else if (key.name === 'backspace') {
          prevStep();
        }
        break;
      }

      case 'credentials': {
        const providers = needsCredentials();
        if (credEditing()) {
          if (key.name === 'return') {
            // Save the edit
            const provider = providers[credCursor()]!;
            if (provider.requiresKey) {
              setCredential(provider.id, 'apiKey', credEditValue());
            }
            setCredEditing(false);
            setCredEditValue('');
          } else if (key.name === 'backspace') {
            setCredEditValue(v => v.slice(0, -1));
          } else if (key.name === 'escape') {
            setCredEditing(false);
            setCredEditValue('');
          } else if (input && input.length === 1 && !key.ctrl) {
            setCredEditValue(v => v + input);
          }
        } else {
          if (key.name === 'up') {
            setCredCursor(c => Math.max(0, c - 1));
          } else if (key.name === 'down') {
            setCredCursor(c => Math.min(providers.length - 1, c + 1));
          } else if (key.name === 'return') {
            const provider = providers[credCursor()]!;
            if (provider.requiresKey) {
              setCredEditing(true);
              const existing = state.credentials[provider.id]?.apiKey ?? '';
              setCredEditValue(existing);
            }
          } else if (key.name === 'tab') {
            // Skip — use env var
            const provider = providers[credCursor()]!;
            if (provider.keyEnvVar) {
              setCredential(provider.id, 'apiKey', `env:${provider.keyEnvVar}`);
            }
            if (credCursor() < providers.length - 1) {
              setCredCursor(c => c + 1);
            } else {
              nextStep();
            }
          } else if (key.name === 'backspace') {
            prevStep();
          }
        }
        break;
      }

      case 'slots': {
        const options = modelOptions();
        if (key.name === 'up') {
          setModelCursor(c => Math.max(0, c - 1));
        } else if (key.name === 'down') {
          setModelCursor(c => Math.min(options.length - 1, c + 1));
        } else if (key.name === 'return') {
          // Assign model to current slot
          const option = options[modelCursor()];
          if (option) {
            setSlotConfig(activeSlot(), option.provider, option.model);
          }
          // Move to next slot or next step
          if (activeSlotIndex() < slotNames.length - 1) {
            setActiveSlotIndex(i => i + 1);
            setModelCursor(0);
          } else {
            nextStep();
          }
        } else if (key.name === 'backspace') {
          if (activeSlotIndex() > 0) {
            setActiveSlotIndex(i => i - 1);
            setModelCursor(0);
          } else {
            prevStep();
          }
        }
        break;
      }

      case 'summary': {
        if (key.name === 'return') {
          handleSave();
        } else if (key.name === 'backspace') {
          prevStep();
        }
        break;
      }
    }
  }

  const configPath = () => {
    const cwd = process.cwd();
    return `${cwd}/iron-rain.json`;
  };

  return (
    <box flexDirection="column" width="100%" height="100%">
      {/* Step indicator */}
      <box flexDirection="row" paddingX={4} paddingY={0} gap={1}>
        {STEPS.map((s, i) => {
          const isCurrent = () => s === state.step;
          const isPast = () => STEPS.indexOf(state.step) > i;
          return (
            <box flexDirection="row" gap={0}>
              <text
                color={isCurrent() ? ironRainTheme.brand.primary : isPast() ? ironRainTheme.status.success : ironRainTheme.chrome.dimFg}
                bold={isCurrent()}
              >
                {isPast() ? '✓' : `${i + 1}`}
              </text>
              <text color={ironRainTheme.chrome.dimFg}>
                {i < STEPS.length - 1 ? ' → ' : ''}
              </text>
            </box>
          );
        })}
      </box>

      {/* Current step */}
      {state.step === 'welcome' && (
        <Welcome onNext={() => nextStep()} />
      )}
      {state.step === 'providers' && (
        <ProviderSelect
          providers={state.providers}
          cursorIndex={providerCursor()}
          onToggle={toggleProvider}
          onNext={() => nextStep()}
          onBack={() => prevStep()}
        />
      )}
      {state.step === 'credentials' && (
        <Credentials
          providers={state.providers}
          credentials={state.credentials}
          cursorIndex={credCursor()}
          editing={credEditing()}
          editValue={credEditValue()}
          onNext={() => nextStep()}
          onBack={() => prevStep()}
        />
      )}
      {state.step === 'slots' && (
        <SlotAssignment
          providers={state.providers}
          slots={state.slots}
          activeSlot={activeSlot()}
          modelCursorIndex={modelCursor()}
          onNext={() => nextStep()}
          onBack={() => prevStep()}
        />
      )}
      {state.step === 'summary' && (
        <Summary
          providers={state.providers}
          credentials={state.credentials}
          slots={state.slots}
          configPath={configPath()}
          onSave={handleSave}
          onBack={() => prevStep()}
        />
      )}
    </box>
  );
}

export { OnboardingWizard as default };
