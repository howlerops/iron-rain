import type { CliPermissionMode } from "../config/schema.js";
import type { EpisodeSummary } from "../episodes/protocol.js";
import {
  episodeRelevance,
  extractKeywords,
  formatEpisodeInputs,
} from "../episodes/protocol.js";
import type { ModelSlotManager } from "../slots/slot-manager.js";
import type { SlotName } from "../slots/types.js";
import { generateId } from "../utils/id.js";
import type { OrchestratorTask } from "./types.js";
import { taskToEpisode } from "./types.js";
import { SlotWorker } from "./worker.js";

export class OrchestratorKernel {
  private slots: ModelSlotManager;
  private workers: Map<SlotName, SlotWorker> = new Map();
  private episodes: EpisodeSummary[] = [];
  private cliPermissions?: Record<string, CliPermissionMode>;

  constructor(
    slots: ModelSlotManager,
    cliPermissions?: Record<string, CliPermissionMode>,
  ) {
    this.slots = slots;
    this.cliPermissions = cliPermissions;
    this.initWorkers();
  }

  private initWorkers(): void {
    const assignment = this.slots.getAllSlots();
    for (const [name, config] of Object.entries(assignment)) {
      this.workers.set(
        name as SlotName,
        new SlotWorker(name as SlotName, config, this.cliPermissions),
      );
    }
  }

  async dispatch(task: OrchestratorTask): Promise<EpisodeSummary> {
    const slotName =
      task.targetSlot ??
      (task.toolType ? this.slots.getSlotForTool(task.toolType) : "main");

    const worker = this.workers.get(slotName);
    if (!worker) {
      throw new Error(`No worker for slot: ${slotName}`);
    }

    // Compose input episodes into prompt for thread-to-thread handoffs
    const enrichedTask = this.enrichTaskWithEpisodes(task);

    const result = await worker.execute(enrichedTask);
    const episode = taskToEpisode(task, result);
    this.episodes.push(episode);
    return episode;
  }

  async *dispatchStreaming(
    task: OrchestratorTask,
    signal?: AbortSignal,
  ): AsyncGenerator<{
    type: "text" | "thinking" | "tool_use" | "error" | "done";
    content: string;
    slot: SlotName;
    tokens?: { input: number; output: number };
    toolCall?: { id: string; name: string; status: "start" | "end" };
  }> {
    const slotName =
      task.targetSlot ??
      (task.toolType ? this.slots.getSlotForTool(task.toolType) : "main");

    const worker = this.workers.get(slotName);
    if (!worker) {
      throw new Error(`No worker for slot: ${slotName}`);
    }

    // Compose input episodes for streaming dispatches too
    const enrichedTask = this.enrichTaskWithEpisodes(task);

    for await (const chunk of worker.stream(enrichedTask, signal)) {
      yield { ...chunk, slot: slotName };
    }
  }

  async orchestrate(tasks: OrchestratorTask[]): Promise<EpisodeSummary[]> {
    const results = await Promise.allSettled(
      tasks.map((t) => this.dispatch(t)),
    );

    return results
      .filter(
        (r): r is PromiseFulfilledResult<EpisodeSummary> =>
          r.status === "fulfilled",
      )
      .map((r) => r.value);
  }

  /**
   * Handoff: dispatch to a specific slot with prior episode(s) as context.
   * This is the thread composition primitive described in the Slate blog:
   * one thread's episode becomes another thread's input context.
   */
  async handoff(
    fromEpisodes: EpisodeSummary | EpisodeSummary[],
    toSlot: SlotName,
    prompt: string,
    systemPrompt?: string,
  ): Promise<EpisodeSummary> {
    const episodes = Array.isArray(fromEpisodes)
      ? fromEpisodes
      : [fromEpisodes];
    return this.dispatch({
      id: generateId(),
      prompt,
      targetSlot: toSlot,
      inputEpisodes: episodes,
      systemPrompt,
    });
  }

  /**
   * RLM-style retrieval: find past episodes relevant to the current prompt.
   * Uses keyword overlap scoring to surface the most contextually relevant
   * episodes from session history.
   */
  retrieveRelevantEpisodes(prompt: string, maxCount = 3): EpisodeSummary[] {
    if (this.episodes.length === 0) return [];

    const queryKeywords = extractKeywords(prompt);
    if (queryKeywords.size === 0) return [];

    const scored = this.episodes.map((ep) => ({
      ep,
      score: episodeRelevance(queryKeywords, ep),
    }));

    scored.sort((a, b) => b.score - a.score);
    return scored
      .slice(0, maxCount)
      .filter((s) => s.score > 0)
      .map((s) => s.ep);
  }

  integrate(episodes: EpisodeSummary[]): void {
    this.episodes.push(...episodes);
  }

  getEpisodes(): readonly EpisodeSummary[] {
    return this.episodes;
  }

  getWorker(slot: SlotName): SlotWorker | undefined {
    return this.workers.get(slot);
  }

  refreshWorkers(): void {
    this.workers.clear();
    this.initWorkers();
  }

  /**
   * Inject input episodes into the task prompt for thread composition.
   * When a task includes inputEpisodes, their compressed content is
   * prepended to the prompt so the receiving thread inherits the
   * conclusions and work history of prior threads.
   */
  private enrichTaskWithEpisodes(task: OrchestratorTask): OrchestratorTask {
    if (!task.inputEpisodes?.length) return task;

    const episodeContext = formatEpisodeInputs(task.inputEpisodes);
    return {
      ...task,
      prompt: `${episodeContext}\n\n${task.prompt}`,
    };
  }
}
