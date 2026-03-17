import type { EpisodeSummary } from "../episodes/protocol.js";
import type { ModelSlotManager } from "../slots/slot-manager.js";
import type { SlotName } from "../slots/types.js";
import type { OrchestratorTask, WorkerResult } from "./types.js";
import { taskToEpisode } from "./types.js";
import { SlotWorker } from "./worker.js";

export class OrchestratorKernel {
  private slots: ModelSlotManager;
  private workers: Map<SlotName, SlotWorker> = new Map();
  private episodes: EpisodeSummary[] = [];

  constructor(slots: ModelSlotManager) {
    this.slots = slots;
    this.initWorkers();
  }

  private initWorkers(): void {
    const assignment = this.slots.getAllSlots();
    for (const [name, config] of Object.entries(assignment)) {
      this.workers.set(
        name as SlotName,
        new SlotWorker(name as SlotName, config),
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

    const result = await worker.execute(task);
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

    for await (const chunk of worker.stream(task, signal)) {
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
}
