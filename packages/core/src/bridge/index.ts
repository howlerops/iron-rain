export type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';
export { OpenAICompatBridge } from './openai-compat.js';
export { OllamaBridge } from './ollama.js';
export { AnthropicBridge } from './anthropic.js';
export { ClaudeCodeBridge } from './claude-code.js';
export { CodexBridge } from './codex.js';
export { GeminiBridge } from './gemini.js';
export { GeminiCLIBridge } from './gemini-cli.js';

import type { CLIBridge } from './types.js';
import type { SlotConfig } from '../slots/types.js';
import { OpenAICompatBridge } from './openai-compat.js';
import { OllamaBridge } from './ollama.js';
import { AnthropicBridge } from './anthropic.js';
import { ClaudeCodeBridge } from './claude-code.js';
import { CodexBridge } from './codex.js';
import { GeminiBridge } from './gemini.js';
import { GeminiCLIBridge } from './gemini-cli.js';

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
    case 'claude-code':
      return new ClaudeCodeBridge({
        model: slot.model,
        binaryPath: slot.apiBase,
      });
    case 'codex':
      return new CodexBridge({
        model: slot.model,
        binaryPath: slot.apiBase,
      });
    case 'gemini':
      return new GeminiBridge({
        apiKey: slot.apiKey ?? 'env:GEMINI_API_KEY',
        model: slot.model,
      });
    case 'gemini-cli':
      return new GeminiCLIBridge({
        model: slot.model,
        binaryPath: slot.apiBase,
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
