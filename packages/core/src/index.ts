// Bridge
export { AnthropicBridge } from "./bridge/anthropic.js";
export { BaseAPIBridge } from "./bridge/base-api.js";
export { BaseCLIBridge } from "./bridge/base-cli.js";
export { ClaudeCodeBridge } from "./bridge/claude-code.js";
export { CodexBridge } from "./bridge/codex.js";
export { GeminiBridge } from "./bridge/gemini.js";
export { GeminiCLIBridge } from "./bridge/gemini-cli.js";
export { createBridgeForSlot } from "./bridge/index.js";
export { OllamaBridge } from "./bridge/ollama.js";
export { OpenAICompatBridge } from "./bridge/openai-compat.js";
export type {
  BridgeChunk,
  BridgeOptions,
  BridgeResult,
  ChatMessage,
  CLIBridge,
  ImageContent,
  MessageContent,
  TextContent,
} from "./bridge/types.js";
export { getTextContent } from "./bridge/types.js";
export {
  BridgeError,
  CircuitBreaker,
  createBridgeError,
  backoffDelay,
  DEFAULT_RETRY_CONFIG,
} from "./bridge/errors.js";
export type { RetryConfig } from "./bridge/errors.js";

// Config
export { findConfigFile, loadConfig, writeConfig } from "./config/loader.js";
export type { IronRainConfig } from "./config/schema.js";
export {
  IronRainConfigSchema,
  parseConfig,
  resolveEnvValue,
} from "./config/schema.js";
export { fetchRemoteConfig, deepMergeConfig, clearRemoteConfigCache } from "./config/remote.js";
export type { RemoteConfigResult } from "./config/remote.js";

// Context
export type {
  CompactedMessage,
  CompactionConfig,
  ContextWindow,
} from "./context/compaction.js";
export {
  buildContextWindow,
  DEFAULT_COMPACTION_CONFIG,
} from "./context/compaction.js";
export type { ParsedInput, ResolvedReference } from "./context/references.js";
export { parseReferences } from "./context/references.js";
export { loadIgnoreRules } from "./context/ignore.js";
export type { IgnoreFilter } from "./context/ignore.js";
export { shouldSummarize, truncateWithContext } from "./context/summarizer.js";

// Commands
export { loadCustomCommands, expandTemplate } from "./commands/index.js";
export type { CustomCommand } from "./commands/index.js";

// Episodes
export type { EpisodeSummary } from "./episodes/protocol.js";
export { createEpisodeSummary } from "./episodes/protocol.js";

// Git
export * from "./git/index.js";

// Input
export { isVoiceAvailable, recordAudio, transcribeAudio, DEFAULT_VOICE_CONFIG } from "./input/index.js";
export type { VoiceConfig } from "./input/index.js";

// LSP
export { LSPClient, detectLanguageServers, DEFAULT_LSP_CONFIG } from "./lsp/index.js";
export type { Diagnostic, LSPClientConfig, LSPServerConfig } from "./lsp/index.js";

// MCP
export { MCPClient } from "./mcp/client.js";
export type { MCPManagerConfig } from "./mcp/manager.js";
export { MCPManager } from "./mcp/manager.js";
export type { MCPServerConfig, MCPTool, MCPToolResult } from "./mcp/types.js";
export {
  namespaceDuplicateTools,
  resolveToolName,
  reconnectDelay,
  DEFAULT_RECONNECT_CONFIG,
} from "./mcp/reconnect.js";
export type { ReconnectConfig } from "./mcp/reconnect.js";

// Memory
export { AutoLearner } from "./memory/index.js";
export type { Lesson } from "./memory/auto-learner.js";

// Orchestrator
export { OrchestratorKernel } from "./orchestrator/kernel.js";
export {
  buildEpisodeContext,
  buildSystemPrompt,
  structuredPrompt,
} from "./orchestrator/prompts.js";
export type { SystemPromptContext } from "./orchestrator/prompts.js";
export type { OrchestratorTask, WorkerResult } from "./orchestrator/types.js";
export { SlotWorker } from "./orchestrator/worker.js";
export { runParallel, DEFAULT_PARALLEL_CONFIG } from "./orchestrator/parallel.js";
export type {
  ParallelChunk,
  ParallelConfig,
  ParallelResult,
  ParallelTask,
} from "./orchestrator/parallel.js";

// Planner
export { PlanExecutor } from "./planner/executor.js";
export { PlanGenerator } from "./planner/generator.js";
export { RalphLoop } from "./planner/ralph-loop.js";
export { PlanStorage } from "./planner/storage.js";
export { LoopStorage } from "./planner/loop-storage.js";
export type {
  LoopCallbacks,
  LoopConfig,
  LoopIteration,
  LoopState,
  Plan,
  PlanCallbacks,
  PlanStatus,
  PlanTask,
  TaskStatus,
} from "./planner/types.js";

// Plugins
export { HookEmitter } from "./plugins/index.js";
export type { HookEvent, HookEventData, HookHandler } from "./plugins/index.js";
export { PluginManager } from "./plugins/index.js";
export type { PluginManagerConfig } from "./plugins/index.js";
export type { PluginModule } from "./plugins/index.js";
export { loadPlugins } from "./plugins/index.js";

// Providers
export { CostRegistry } from "./providers/cost-registry.js";
export { ModelRegistry } from "./providers/model-registry.js";
export type { ProviderInfo } from "./providers/registry.js";
export { ProviderRegistry } from "./providers/registry.js";

// Repo Map
export { generateRepoMap } from "./repomap/index.js";

// Review
export {
  buildReviewPrompt,
  getBranchDiff,
  getStagedDiff,
  parseReviewResponse,
} from "./review/index.js";
export type { ReviewIssue, ReviewResult } from "./review/index.js";

// Router
export { detectToolType } from "./router/heuristics.js";
export { getSlotForTool, getToolsForSlot } from "./router/tool-router.js";

// Rules
export { loadProjectRules } from "./rules/index.js";

// Sandbox
export {
  DEFAULT_SANDBOX_CONFIG,
  detectAvailableBackends,
  DockerExecutor,
  getSandboxExecutor,
  GvisorExecutor,
  registerSandboxBackend,
  SeatbeltExecutor,
  wrapCommandForSandbox,
} from "./sandbox/index.js";
export type {
  SandboxBackend,
  SandboxConfig,
  SandboxExecutor,
  SandboxResult,
} from "./sandbox/index.js";
export type { DockerConfig } from "./sandbox/index.js";
export { DEFAULT_DOCKER_CONFIG } from "./sandbox/index.js";

// Skills
export { SkillExecutor } from "./skills/executor.js";
export {
  discoverSkills,
  getDiscoveryPaths,
  loadSkillFile,
} from "./skills/loader.js";
export { SkillRegistry } from "./skills/registry.js";
export type { Skill, SkillDiscoveryPath } from "./skills/types.js";

// Slots
export { DEFAULT_SLOT_ASSIGNMENT } from "./slots/defaults.js";
export { ModelSlotManager } from "./slots/slot-manager.js";
export type {
  SlotAssignment,
  SlotConfig,
  SlotName,
  ThinkingLevel,
  ToolType,
} from "./slots/types.js";
export { SLOT_NAMES } from "./slots/types.js";

// Updater
export type {
  DoctorCheck,
  UpdateCheckResult,
  UpdateResult,
  VersionInfo,
} from "./updater/index.js";
export {
  checkForUpdate,
  getCurrentVersion,
  getVersionInfo,
  isNewerVersion,
  performUpdate,
  runDiagnostics,
} from "./updater/index.js";
