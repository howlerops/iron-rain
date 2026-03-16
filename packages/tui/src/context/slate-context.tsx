import { createContext, useContext, type JSX } from 'solid-js';
import { createStore } from 'solid-js/store';
import type { SlotAssignment, SlotName, IronRainConfig } from '@howlerops/iron-rain';
import { DEFAULT_SLOT_ASSIGNMENT } from '@howlerops/iron-rain';
import type { Message } from '../components/session-view.js';

export interface SlateState {
  slots: SlotAssignment;
  activeSlot: SlotName;
  messages: Message[];
  isLoading: boolean;
  showSplash: boolean;
  config: IronRainConfig | null;
}

export interface SlateActions {
  setActiveSlot: (slot: SlotName) => void;
  addMessage: (msg: Message) => void;
  setLoading: (loading: boolean) => void;
  dismissSplash: () => void;
  updateSlots: (slots: Partial<SlotAssignment>) => void;
}

type SlateContextValue = [SlateState, SlateActions];

const SlateContext = createContext<SlateContextValue>();

export function SlateProvider(props: { config?: IronRainConfig; children: JSX.Element }) {
  const [state, setState] = createStore<SlateState>({
    slots: (props.config?.slots as SlotAssignment) ?? DEFAULT_SLOT_ASSIGNMENT,
    activeSlot: 'main',
    messages: [],
    isLoading: false,
    showSplash: true,
    config: props.config ?? null,
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
