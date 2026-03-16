import type { SlotConfig } from '../slots/types.js';

export interface ProviderInfo {
  name: string;
  apiBase?: string;
  models: string[];
}

const KNOWN_PROVIDERS: ProviderInfo[] = [
  {
    name: 'anthropic',
    models: [
      'claude-opus-4-6',
      'claude-sonnet-4-6',
      'claude-sonnet-4-20250514',
      'claude-haiku-4-5-20251001',
    ],
  },
  {
    name: 'openai',
    apiBase: 'https://api.openai.com/v1',
    models: ['gpt-4o', 'gpt-4o-mini', 'o3', 'o4-mini'],
  },
  {
    name: 'ollama',
    apiBase: 'http://localhost:11434',
    models: ['qwen2.5-coder:32b', 'llama3.3:70b', 'deepseek-coder-v2:latest'],
  },
  {
    name: 'claude-code',
    models: ['opus', 'sonnet', 'haiku'],
  },
  {
    name: 'codex',
    models: ['o3', 'o4-mini', 'gpt-4o'],
  },
  {
    name: 'gemini',
    models: ['gemini-2.5-pro', 'gemini-2.5-flash', 'gemini-2.0-flash'],
  },
  {
    name: 'gemini-cli',
    models: ['gemini-2.5-pro', 'gemini-2.5-flash', 'gemini-2.0-flash'],
  },
];

export class ProviderRegistry {
  private providers: Map<string, ProviderInfo> = new Map();

  constructor() {
    for (const p of KNOWN_PROVIDERS) {
      this.providers.set(p.name, p);
    }
  }

  get(name: string): ProviderInfo | undefined {
    return this.providers.get(name);
  }

  register(provider: ProviderInfo): void {
    this.providers.set(provider.name, provider);
  }

  list(): ProviderInfo[] {
    return [...this.providers.values()];
  }

  getModelsForProvider(name: string): string[] {
    return this.providers.get(name)?.models ?? [];
  }

  toSlotConfig(provider: string, model: string): SlotConfig {
    const info = this.providers.get(provider);
    return {
      provider,
      model,
      apiBase: info?.apiBase,
    };
  }
}
