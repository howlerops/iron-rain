export type SlotName = 'main' | 'explore' | 'execute';

export type ToolType =
  | 'edit'
  | 'write'
  | 'bash'
  | 'grep'
  | 'glob'
  | 'read'
  | 'search'
  | 'strategy'
  | 'plan'
  | 'conversation';

export interface SlotConfig {
  provider: string;
  model: string;
  apiKey?: string;
  apiBase?: string;
}

export type SlotAssignment = Record<SlotName, SlotConfig>;

export const SLOT_NAMES: readonly SlotName[] = ['main', 'explore', 'execute'] as const;
