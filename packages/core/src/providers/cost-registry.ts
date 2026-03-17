/**
 * Model cost tracking — estimates costs based on token usage and known pricing.
 * Prices are per million tokens.
 */

interface ModelPricing {
  input: number; // $ per million input tokens
  output: number; // $ per million output tokens
}

const DEFAULT_PRICING: Record<string, ModelPricing> = {
  // Anthropic
  "claude-opus-4-6": { input: 15, output: 75 },
  "claude-opus-4-20250514": { input: 15, output: 75 },
  "claude-sonnet-4-6": { input: 3, output: 15 },
  "claude-sonnet-4-20250514": { input: 3, output: 15 },
  "claude-haiku-4-5": { input: 0.8, output: 4 },
  "claude-haiku-4-5-20251001": { input: 0.8, output: 4 },

  // OpenAI
  "gpt-4o": { input: 2.5, output: 10 },
  "gpt-4o-mini": { input: 0.15, output: 0.6 },
  o3: { input: 10, output: 40 },
  "o3-mini": { input: 1.1, output: 4.4 },
  "o4-mini": { input: 1.1, output: 4.4 },

  // Google
  "gemini-2.5-pro": { input: 1.25, output: 10 },
  "gemini-2.5-flash": { input: 0.15, output: 0.6 },
};

export class CostRegistry {
  private customPricing: Record<string, ModelPricing> = {};

  /**
   * Set custom pricing for a model.
   */
  setPrice(model: string, pricing: ModelPricing): void {
    this.customPricing[model] = pricing;
  }

  /**
   * Load custom pricing from config.
   */
  loadFromConfig(
    costs?: Record<string, { input: number; output: number }>,
  ): void {
    if (!costs) return;
    for (const [model, pricing] of Object.entries(costs)) {
      this.customPricing[model] = pricing;
    }
  }

  /**
   * Get pricing for a model. Returns null if unknown.
   */
  getPricing(model: string): ModelPricing | null {
    // Check custom first, then defaults
    if (this.customPricing[model]) return this.customPricing[model];
    if (DEFAULT_PRICING[model]) return DEFAULT_PRICING[model];

    // Try partial match (e.g., "claude-sonnet" matches "claude-sonnet-4-20250514")
    for (const [key, pricing] of Object.entries({
      ...DEFAULT_PRICING,
      ...this.customPricing,
    })) {
      if (model.startsWith(key) || key.startsWith(model)) {
        return pricing;
      }
    }

    return null;
  }

  /**
   * Estimate cost for a given model and token counts.
   */
  estimateCost(
    model: string,
    tokens: { input: number; output: number },
  ): number | null {
    const pricing = this.getPricing(model);
    if (!pricing) return null;

    const inputCost = (tokens.input / 1_000_000) * pricing.input;
    const outputCost = (tokens.output / 1_000_000) * pricing.output;
    return inputCost + outputCost;
  }

  /**
   * Format a cost value as a dollar string.
   */
  static formatCost(cost: number): string {
    if (cost < 0.01) return `$${cost.toFixed(4)}`;
    if (cost < 1) return `$${cost.toFixed(3)}`;
    return `$${cost.toFixed(2)}`;
  }
}
