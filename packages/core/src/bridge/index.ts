export type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';
export { OpenAICompatBridge } from './openai-compat.js';
export { OllamaBridge } from './ollama.js';
export { AnthropicBridge } from './anthropic.js';

import type { CLIBridge } from './types.js';
import type { SlotConfig } from '../slots/types.js';
import { OpenAICompatBridge } from './openai-compat.js';
import { OllamaBridge } from './ollama.js';
import { AnthropicBridge } from './anthropic.js';

export function createBridgeForSlot(slot: SlotConfig): CLIBridge {
  switch (slot.provider) {
    case 'ollama':
      return new OllamaBridge({
        apiBase: slot.apiBase,
        model: slot.model,
      });
    case 'anthropic':
      return new AnthropicBridge({
        apiKey: slot.apiKey ?? 'env:ANTHROPIC_API_KEY',
        model: slot.model,
      });
    default:
      return new OpenAICompatBridge({
        name: slot.provider,
        apiBase: slot.apiBase ?? 'https://api.openai.com/v1',
        apiKey: slot.apiKey ?? 'env:OPENAI_API_KEY',
        model: slot.model,
      });
  }
}
