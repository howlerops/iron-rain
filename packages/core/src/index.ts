// Slots
export type { SlotName, SlotConfig, SlotAssignment, ToolType } from './slots/types.js';
export { SLOT_NAMES } from './slots/types.js';
export { ModelSlotManager } from './slots/slot-manager.js';
export { DEFAULT_SLOT_ASSIGNMENT } from './slots/defaults.js';

// Router
export { getSlotForTool, getToolsForSlot } from './router/tool-router.js';

// Episodes
export type { EpisodeSummary } from './episodes/protocol.js';
export { createEpisodeSummary } from './episodes/protocol.js';

// Bridge
export type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './bridge/types.js';
export { OpenAICompatBridge } from './bridge/openai-compat.js';
export { OllamaBridge } from './bridge/ollama.js';
export { AnthropicBridge } from './bridge/anthropic.js';
export { createBridgeForSlot } from './bridge/index.js';

// Orchestrator
export type { OrchestratorTask, WorkerResult } from './orchestrator/types.js';
export { OrchestratorKernel } from './orchestrator/kernel.js';
export { SlotWorker } from './orchestrator/worker.js';

// Config
export type { IronRainConfig } from './config/schema.js';
export { IronRainConfigSchema, parseConfig, resolveEnvValue } from './config/schema.js';
export { loadConfig, findConfigFile } from './config/loader.js';

// Providers
export { ProviderRegistry } from './providers/registry.js';
export type { ProviderInfo } from './providers/registry.js';

// State
export { Signal, createSignal } from './state/store.js';
