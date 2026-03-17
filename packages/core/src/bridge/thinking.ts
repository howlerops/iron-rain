import type { ThinkingLevel } from "../slots/types.js";

/** Returns Anthropic `budget_tokens` value, or null if thinking is off. */
export function anthropicThinkingBudget(level: ThinkingLevel): number | null {
  switch (level) {
    case "low":
      return 4096;
    case "medium":
      return 16384;
    case "high":
      return 32768;
    default:
      return null;
  }
}

/** Returns OpenAI `reasoning_effort` value, or null if thinking is off. */
export function openaiReasoningEffort(level: ThinkingLevel): string | null {
  switch (level) {
    case "low":
      return "low";
    case "medium":
      return "medium";
    case "high":
      return "high";
    default:
      return null;
  }
}

/** Returns Gemini `thinkingBudget` value, or null if thinking is off. */
export function geminiThinkingBudget(level: ThinkingLevel): number | null {
  switch (level) {
    case "off":
      return 0;
    case "low":
      return 4096;
    case "medium":
      return 12288;
    case "high":
      return 24576;
    default:
      return null;
  }
}
