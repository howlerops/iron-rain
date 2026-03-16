import { createContext, useContext, type JSX } from 'solid-js';
import { createStore } from 'solid-js/store';
import type { SlotAssignment, SlotName, IronRainConfig, OrchestratorTask, EpisodeSummary } from '@howlerops/iron-rain';
import { DEFAULT_SLOT_ASSIGNMENT, ModelSlotManager, OrchestratorKernel } from '@howlerops/iron-rain';
import type { Message, SlotActivity, SessionStats } from '../components/session-view.js';

export interface SlateState {
  slots: SlotAssignment;
  activeSlot: SlotName;
  messages: Message[];
  isLoading: boolean;
  showSplash: boolean;
  config: IronRainConfig | null;
  sessionStats: SessionStats;
}

export interface SlateActions {
  setActiveSlot: (slot: SlotName) => void;
  addMessage: (msg: Message) => void;
  setLoading: (loading: boolean) => void;
  dismissSplash: () => void;
  updateSlots: (slots: Partial<SlotAssignment>) => void;
  dispatch: (prompt: string) => Promise<void>;
}

type SlateContextValue = [SlateState, SlateActions];

const SlateContext = createContext<SlateContextValue>();

export function SlateProvider(props: { config?: IronRainConfig; children: JSX.Element }) {
  const slotAssignment = (props.config?.slots as SlotAssignment) ?? DEFAULT_SLOT_ASSIGNMENT;

  // Create the orchestrator kernel if we have config
  let kernel: OrchestratorKernel | null = null;
  if (props.config?.slots) {
    const slotManager = new ModelSlotManager(slotAssignment);
    kernel = new OrchestratorKernel(slotManager);
  }

  const [state, setState] = createStore<SlateState>({
    slots: slotAssignment,
    activeSlot: 'main',
    messages: [],
    isLoading: false,
    showSplash: true,
    config: props.config ?? null,
    sessionStats: { totalDuration: 0, totalTokens: 0, modelCount: 1, requestCount: 0 },
  });

  const actions: SlateActions = {
    setActiveSlot(slot) {
      setState('activeSlot', slot);
    },
    addMessage(msg) {
      setState('messages', (prev) => [...prev, msg]);
    },
    setLoading(loading) {
      setState('isLoading', loading);
    },
    dismissSplash() {
      setState('showSplash', false);
    },
    updateSlots(slots) {
      setState('slots', (prev) => ({ ...prev, ...slots }));
    },
    async dispatch(prompt: string) {
      if (!kernel) {
        const slotManager = new ModelSlotManager(state.slots);
        kernel = new OrchestratorKernel(slotManager);
      }

      setState('isLoading', true);
      setState('activeSlot', 'main');

      try {
        const task: OrchestratorTask = {
          id: crypto.randomUUID?.() ?? `${Date.now()}`,
          prompt,
          targetSlot: 'main',
        };

        const episode: EpisodeSummary = await kernel.dispatch(task);

        // Build slot activity record
        const activity: SlotActivity = {
          slot: episode.slot,
          task: prompt.length > 60 ? prompt.slice(0, 57) + '...' : prompt,
          status: episode.status === 'failure' ? 'error' : 'done',
          duration: episode.duration,
          tokens: episode.tokens,
        };

        setState('messages', (prev) => [...prev, {
          id: episode.id,
          role: 'assistant' as const,
          content: episode.status === 'failure'
            ? `**Error:** ${episode.result}`
            : episode.result,
          slot: episode.slot,
          timestamp: Date.now(),
          activities: [activity],
          tokens: episode.tokens,
          duration: episode.duration,
        }]);

        // Update cumulative stats
        setState('sessionStats', (prev) => ({
          totalDuration: prev.totalDuration + episode.duration,
          totalTokens: prev.totalTokens + episode.tokens,
          modelCount: prev.modelCount,
          requestCount: prev.requestCount + 1,
        }));
      } catch (err) {
        setState('messages', (prev) => [...prev, {
          id: `err-${Date.now()}`,
          role: 'assistant' as const,
          content: `**Error:** ${err instanceof Error ? err.message : String(err)}`,
          slot: 'main' as SlotName,
          timestamp: Date.now(),
        }]);
      } finally {
        setState('isLoading', false);
      }
    },
  };

  return (
    <SlateContext.Provider value={[state, actions]}>{props.children}</SlateContext.Provider>
  );
}

export function useSlate(): SlateContextValue {
  const ctx = useContext(SlateContext);
  if (!ctx) throw new Error('useSlate must be used within SlateProvider');
  return ctx;
}
