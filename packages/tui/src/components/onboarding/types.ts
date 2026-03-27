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

export const PROVIDER_TYPE_LABELS: Record<ProviderChoice["type"], string> = {
  api: "API Providers",
  cli: "CLI Providers",
  local: "Local Providers",
};

export const PROVIDER_TYPE_ORDER: ProviderChoice["type"][] = [
  "api",
  "cli",
  "local",
];

export const AVAILABLE_PROVIDERS: ProviderChoice[] = [
  // API providers (require key)
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
    id: "gemini",
    name: "Gemini",
    description: "Google Gemini via API",
    type: "api",
    selected: false,
    requiresKey: true,
    keyEnvVar: "GEMINI_API_KEY",
  },
  // CLI providers (use existing subscriptions)
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
    id: "gemini-cli",
    name: "Gemini CLI",
    description: "Use your Gemini CLI subscription",
    type: "cli",
    selected: false,
    requiresKey: false,
  },
  // Local providers (free, private)
  {
    id: "ollama",
    name: "Ollama",
    description: "Local models, free, private",
    type: "local",
    selected: false,
    requiresKey: false,
    defaultApiBase: "http://localhost:11434",
  },
];

export const PROVIDER_MODELS: Record<string, string[]> = {
  anthropic: [
    "claude-opus-4-6",
    "claude-sonnet-4-6",
    "claude-haiku-4-5-20251001",
  ],
  openai: ["gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "o3", "o4-mini"],
  "claude-code": ["opus", "sonnet", "haiku"],
  codex: ["gpt-5.4", "o3", "o4-mini"],
  gemini: [
    "gemini-3.1-pro-preview",
    "gemini-3-flash-preview",
    "gemini-2.5-pro",
    "gemini-2.5-flash",
  ],
  "gemini-cli": [
    "gemini-3.1-pro-preview",
    "gemini-3-flash-preview",
    "gemini-2.5-pro",
    "gemini-2.5-flash",
  ],
  ollama: ["qwen2.5-coder:32b", "llama3.3:70b", "deepseek-coder-v2:latest"],
};
