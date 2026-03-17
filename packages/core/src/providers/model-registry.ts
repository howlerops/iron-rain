import type { ProviderInfo } from "./registry.js";

interface CacheEntry {
  models: string[];
  fetchedAt: number;
}

const CACHE_TTL = 5 * 60 * 1000; // 5 minutes
const FETCH_TIMEOUT = 5000; // 5 seconds

/**
 * Fetches available models from providers that support listing,
 * with in-memory caching and hardcoded fallback.
 */
export class ModelRegistry {
  private cache = new Map<string, CacheEntry>();
  private inflight = new Map<string, Promise<string[]>>();
  private hardcoded: Record<string, string[]>;

  constructor(hardcodedModels: Record<string, string[]>) {
    this.hardcoded = hardcodedModels;
  }

  async getModels(
    providerId: string,
    credentials?: { apiKey?: string; apiBase?: string },
  ): Promise<string[]> {
    // Check cache
    const cached = this.cache.get(providerId);
    if (cached && Date.now() - cached.fetchedAt < CACHE_TTL) {
      return cached.models;
    }

    // Deduplicate concurrent fetches
    const existing = this.inflight.get(providerId);
    if (existing) return existing;

    const promise = this.fetchModels(providerId, credentials);
    this.inflight.set(providerId, promise);
    try {
      return await promise;
    } finally {
      this.inflight.delete(providerId);
    }
  }

  private async fetchModels(
    providerId: string,
    credentials?: { apiKey?: string; apiBase?: string },
  ): Promise<string[]> {
    try {
      let models: string[] | null = null;

      switch (providerId) {
        case "ollama":
          models = await this.fetchOllama(
            credentials?.apiBase ?? "http://localhost:11434",
          );
          break;
        case "openai":
          models = await this.fetchOpenAI(
            credentials?.apiBase ?? "https://api.openai.com/v1",
            credentials?.apiKey ?? "",
          );
          break;
        case "gemini":
          models = await this.fetchGemini(credentials?.apiKey ?? "");
          break;
      }

      if (models && models.length > 0) {
        this.cache.set(providerId, { models, fetchedAt: Date.now() });
        return models;
      }
    } catch {
      // Fall through to hardcoded
    }

    return this.hardcoded[providerId] ?? [];
  }

  private async fetchOllama(apiBase: string): Promise<string[]> {
    const res = await fetch(`${apiBase}/api/tags`, {
      signal: AbortSignal.timeout(FETCH_TIMEOUT),
    });
    if (!res.ok) return [];
    const data = (await res.json()) as { models?: Array<{ name: string }> };
    return data.models?.map((m) => m.name) ?? [];
  }

  private async fetchOpenAI(
    apiBase: string,
    apiKey: string,
  ): Promise<string[]> {
    if (!apiKey) return [];
    const res = await fetch(`${apiBase}/models`, {
      headers: { Authorization: `Bearer ${apiKey}` },
      signal: AbortSignal.timeout(FETCH_TIMEOUT),
    });
    if (!res.ok) return [];
    const data = (await res.json()) as { data?: Array<{ id: string }> };
    const ids = data.data?.map((m) => m.id) ?? [];
    return ids.filter((id) => /^(gpt-|o[0-9])/.test(id)).sort();
  }

  private async fetchGemini(apiKey: string): Promise<string[]> {
    if (!apiKey) return [];
    const res = await fetch(
      `https://generativelanguage.googleapis.com/v1/models?key=${apiKey}`,
      { signal: AbortSignal.timeout(FETCH_TIMEOUT) },
    );
    if (!res.ok) return [];
    const data = (await res.json()) as {
      models?: Array<{
        name: string;
        supportedGenerationMethods?: string[];
      }>;
    };
    return (data.models ?? [])
      .filter(
        (m) =>
          m.name.includes("gemini") &&
          m.supportedGenerationMethods?.includes("generateContent"),
      )
      .map((m) => m.name.replace("models/", ""));
  }

  clearCache(providerId?: string) {
    if (providerId) {
      this.cache.delete(providerId);
    } else {
      this.cache.clear();
    }
  }
}
