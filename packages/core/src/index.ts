// Slots
export type { SlotName, SlotConfig, SlotAssignment, ToolType, ThinkingLevel } from './slots/types.js';
export { SLOT_NAMES } from './slots/types.js';
export { ModelSlotManager } from './slots/slot-manager.js';
export { DEFAULT_SLOT_ASSIGNMENT } from './slots/defaults.js';

// Router
export { getSlotForTool, getToolsForSlot } from './router/tool-router.js';
export { detectToolType } from './router/heuristics.js';

// Episodes
export type { EpisodeSummary } from './episodes/protocol.js';
export { createEpisodeSummary } from './episodes/protocol.js';

// Bridge
export type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk, ChatMessage, MessageContent, ImageContent, TextContent } from './bridge/types.js';
export { getTextContent } from './bridge/types.js';
export { OpenAICompatBridge } from './bridge/openai-compat.js';
export { OllamaBridge } from './bridge/ollama.js';
export { AnthropicBridge } from './bridge/anthropic.js';
export { ClaudeCodeBridge } from './bridge/claude-code.js';
export { CodexBridge } from './bridge/codex.js';
export { GeminiBridge } from './bridge/gemini.js';
export { GeminiCLIBridge } from './bridge/gemini-cli.js';
export { createBridgeForSlot } from './bridge/index.js';

// Orchestrator
export type { OrchestratorTask, WorkerResult } from './orchestrator/types.js';
export { OrchestratorKernel } from './orchestrator/kernel.js';
export { SlotWorker } from './orchestrator/worker.js';
export { buildSystemPrompt, buildEpisodeContext, structuredPrompt } from './orchestrator/prompts.js';

// Config
export type { IronRainConfig } from './config/schema.js';
export { IronRainConfigSchema, parseConfig, resolveEnvValue } from './config/schema.js';
export { loadConfig, findConfigFile, writeConfig } from './config/loader.js';

// Providers
export { ProviderRegistry } from './providers/registry.js';
export type { ProviderInfo } from './providers/registry.js';
export { ModelRegistry } from './providers/model-registry.js';

// Context compaction (RLM)
export { buildContextWindow, DEFAULT_COMPACTION_CONFIG } from './context/compaction.js';
export type { ContextWindow, CompactionConfig, CompactedMessage } from './context/compaction.js';

// Context references (@ mentions)
export { parseReferences } from './context/references.js';
export type { ResolvedReference, ParsedInput } from './context/references.js';

// Updater
export {
  checkForUpdate,
  performUpdate,
  isNewerVersion,
  getCurrentVersion,
  getVersionInfo,
  runDiagnostics,
} from './updater/index.js';
export type { UpdateCheckResult, UpdateResult, VersionInfo, DoctorCheck } from './updater/index.js';

// MCP
export { MCPClient } from './mcp/client.js';
export { MCPManager } from './mcp/manager.js';
export type { MCPManagerConfig } from './mcp/manager.js';
export type { MCPServerConfig, MCPTool, MCPToolResult } from './mcp/types.js';

// Skills
export { SkillRegistry } from './skills/registry.js';
export { SkillExecutor } from './skills/executor.js';
export { discoverSkills, loadSkillFile, getDiscoveryPaths } from './skills/loader.js';
export type { Skill, SkillDiscoveryPath } from './skills/types.js';

// Planner
export { PlanGenerator } from './planner/generator.js';
export { PlanExecutor } from './planner/executor.js';
export { PlanStorage } from './planner/storage.js';
export { RalphLoop } from './planner/ralph-loop.js';
export type {
  Plan, PlanTask, PlanStatus, TaskStatus, PlanCallbacks,
  LoopConfig, LoopIteration, LoopState, LoopCallbacks,
} from './planner/types.js';

// State
export { Signal, createSignal } from './state/store.js';
