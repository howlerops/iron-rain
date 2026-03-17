import type { EpisodeSummary } from "../episodes/protocol.js";
import type { SlotName, ToolType } from "../slots/types.js";
import { generateId } from "../utils/id.js";

export interface OrchestratorTask {
  id: string;
  prompt: string;
  toolType?: ToolType;
  targetSlot?: SlotName;
  systemPrompt?: string;
  history?: Array<{ role: "user" | "assistant"; content: string }>;
  metadata?: Record<string, unknown>;
}

export interface WorkerResult {
  taskId: string;
  slot: SlotName;
  content: string;
  tokens: { input: number; output: number };
  duration: number;
  filesModified?: string[];
  status: "success" | "failure" | "partial";
  error?: string;
}

export function taskToEpisode(
  task: OrchestratorTask,
  result: WorkerResult,
): EpisodeSummary {
  return {
    id: generateId(),
    slot: result.slot,
    task: task.prompt,
    result: result.content || result.error || "",
    tokens: result.tokens.input + result.tokens.output,
    duration: result.duration,
    filesModified: result.filesModified,
    status: result.status,
  };
}
