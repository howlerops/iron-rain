import type { SlotConfig, SlotName } from "@howlerops/iron-rain";

export type OnboardingStep =
  | "welcome"
  | "providers"
  | "credentials"
  | "slots"
  | "summary";

export interface ProviderChoice {
  id: string;
  name: string;
  description: string;
  type: "api" | "cli" | "local";
  selected: boolean;
  requiresKey: boolean;
  keyEnvVar?: string;
  defaultApiBase?: string;
}

export interface OnboardingState {
  step: OnboardingStep;
  providers: ProviderChoice[];
  credentials: Record<string, { apiKey?: string; apiBase?: string }>;
  slots: Record<SlotName, SlotConfig>;
}

export const AVAILABLE_PROVIDERS: ProviderChoice[] = [
  {
    id: "ollama",
    name: "Ollama",
    description: "Local models, free, private",
    type: "local",
    selected: false,
    requiresKey: false,
    defaultApiBase: "http://localhost:11434",
  },
  {
    id: "anthropic",
    name: "Anthropic",
    description: "Claude models via API",
    type: "api",
    selected: false,
    requiresKey: true,
    keyEnvVar: "ANTHROPIC_API_KEY",
  },
  {
    id: "openai",
    name: "OpenAI",
    description: "GPT models via API",
    type: "api",
    selected: false,
    requiresKey: true,
    keyEnvVar: "OPENAI_API_KEY",
  },
  {
    id: "claude-code",
    name: "Claude Code",
    description: "Use your Claude Code subscription",
    type: "cli",
    selected: false,
    requiresKey: false,
  },
  {
    id: "codex",
    name: "Codex",
    description: "Use your OpenAI Codex subscription",
    type: "cli",
    selected: false,
    requiresKey: false,
  },
  {
    id: "gemini",
    name: "Gemini",
    description: "Google Gemini via API",
    type: "api",
    selected: false,
    requiresKey: true,
    keyEnvVar: "GEMINI_API_KEY",
  },
  {
    id: "gemini-cli",
    name: "Gemini CLI",
    description: "Use your Gemini CLI subscription",
    type: "cli",
    selected: false,
    requiresKey: false,
  },
];

export const PROVIDER_MODELS: Record<string, string[]> = {
  ollama: [
    "llama3.2",
    "qwen2.5-coder:32b",
    "deepseek-coder-v2:latest",
    "mistral",
  ],
  anthropic: [
    "claude-opus-4-6",
    "claude-sonnet-4-20250514",
    "claude-haiku-4-5-20251001",
  ],
  openai: ["gpt-4o", "gpt-4o-mini", "o3", "o4-mini"],
  "claude-code": [
    "claude-sonnet-4-20250514",
    "claude-opus-4-6",
    "claude-haiku-4-5-20251001",
  ],
  codex: ["o3", "o4-mini", "gpt-4o"],
  gemini: ["gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"],
  "gemini-cli": ["gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"],
};
