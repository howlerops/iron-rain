export type SlotName = "main" | "explore" | "execute";

export type ThinkingLevel = "off" | "low" | "medium" | "high";

export type ToolType =
  | "edit"
  | "write"
  | "bash"
  | "grep"
  | "glob"
  | "read"
  | "search"
  | "strategy"
  | "plan"
  | "conversation";

export interface SlotConfig {
  provider: string;
  model: string;
  apiKey?: string;
  apiBase?: string;
  thinkingLevel?: ThinkingLevel;
  systemPrompt?: string;
  fallback?: SlotConfig;
}

export type SlotAssignment = Record<SlotName, SlotConfig>;

export const SLOT_NAMES: readonly SlotName[] = [
  "main",
  "explore",
  "execute",
] as const;
