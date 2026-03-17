/**
 * Parallel task dispatcher — runs multiple OrchestratorTasks concurrently.
 */

import type { BridgeChunk } from "../bridge/types.js";
import type { SlotName } from "../slots/types.js";

export interface ParallelTask {
  prompt: string;
  targetSlot?: SlotName;
  label?: string;
}

export interface ParallelChunk {
  taskIndex: number;
  label: string;
  slot: SlotName;
  chunk: BridgeChunk;
}

export interface ParallelResult {
  taskIndex: number;
  label: string;
  slot: SlotName;
  content: string;
  error?: string;
}

export interface ParallelConfig {
  maxConcurrency: number;
}

export const DEFAULT_PARALLEL_CONFIG: ParallelConfig = {
  maxConcurrency: 4,
};

/**
 * Run multiple tasks with bounded concurrency.
 * Returns results as they complete.
 */
export async function runParallel<T, R>(
  items: T[],
  fn: (item: T, index: number) => Promise<R>,
  maxConcurrency: number,
): Promise<R[]> {
  const results: R[] = new Array(items.length);
  let nextIndex = 0;

  async function worker(): Promise<void> {
    while (nextIndex < items.length) {
      const index = nextIndex++;
      results[index] = await fn(items[index], index);
    }
  }

  const workers = Array.from(
    { length: Math.min(maxConcurrency, items.length) },
    () => worker(),
  );

  await Promise.all(workers);
  return results;
}
