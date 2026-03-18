export { AnthropicBridge } from "./anthropic.js";
export { BaseAPIBridge } from "./base-api.js";
export { BaseCLIBridge } from "./base-cli.js";
export { ClaudeCodeBridge } from "./claude-code.js";
export { CodexBridge } from "./codex.js";
export type { RetryConfig } from "./errors.js";
export { BridgeError, CircuitBreaker } from "./errors.js";
export { GeminiBridge } from "./gemini.js";
export { GeminiCLIBridge } from "./gemini-cli.js";
export { OllamaBridge } from "./ollama.js";
export { OpenAICompatBridge } from "./openai-compat.js";
export type {
  BridgeChunk,
  BridgeOptions,
  BridgeResult,
  ChatMessage,
  CLIBridge,
} from "./types.js";

import type { CliPermissionMode } from "../config/schema.js";
import type { SlotConfig } from "../slots/types.js";
import { AnthropicBridge } from "./anthropic.js";
import { ClaudeCodeBridge } from "./claude-code.js";
import { CodexBridge } from "./codex.js";
import { GeminiBridge } from "./gemini.js";
import { GeminiCLIBridge } from "./gemini-cli.js";
import { OllamaBridge } from "./ollama.js";
import { OpenAICompatBridge } from "./openai-compat.js";
import type { CLIBridge } from "./types.js";

export function createBridgeForSlot(
  slot: SlotConfig,
  cliPermissions?: Record<string, CliPermissionMode>,
): CLIBridge {
  const perm = cliPermissions?.[slot.provider];
  switch (slot.provider) {
    case "ollama":
      return new OllamaBridge({
        apiBase: slot.apiBase,
        model: slot.model,
      });
    case "anthropic":
      return new AnthropicBridge({
        apiKey: slot.apiKey ?? "env:ANTHROPIC_API_KEY",
        model: slot.model,
      });
    case "claude-code":
      return new ClaudeCodeBridge({
        model: slot.model,
        binaryPath: slot.apiBase,
        permissionMode: perm,
      });
    case "codex":
      return new CodexBridge({
        model: slot.model,
        binaryPath: slot.apiBase,
        permissionMode: perm,
      });
    case "gemini":
      return new GeminiBridge({
        apiKey: slot.apiKey ?? "env:GEMINI_API_KEY",
        model: slot.model,
      });
    case "gemini-cli":
      return new GeminiCLIBridge({
        model: slot.model,
        binaryPath: slot.apiBase,
        permissionMode: perm,
      });
    default:
      return new OpenAICompatBridge({
        name: slot.provider,
        apiBase: slot.apiBase ?? "https://api.openai.com/v1",
        apiKey: slot.apiKey ?? "env:OPENAI_API_KEY",
        model: slot.model,
      });
  }
}
