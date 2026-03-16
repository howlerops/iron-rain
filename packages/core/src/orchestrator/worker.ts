import type { SlotConfig, SlotName } from '../slots/types.js';
import type { OrchestratorTask, WorkerResult } from './types.js';
import type { CLIBridge } from '../bridge/types.js';
import { createBridgeForSlot } from '../bridge/index.js';

export class SlotWorker {
  private bridge: CLIBridge;
  private slot: SlotName;

  constructor(slotName: SlotName, slotConfig: SlotConfig) {
    this.slot = slotName;
    this.bridge = createBridgeForSlot(slotConfig);
  }

  async execute(task: OrchestratorTask, signal?: AbortSignal): Promise<WorkerResult> {
    const start = Date.now();

    try {
      const result = await this.bridge.execute(task.prompt, { signal });

      return {
        taskId: task.id,
        slot: this.slot,
        content: result.content,
        tokens: result.tokens,
        duration: result.duration,
        status: 'success',
      };
    } catch (err) {
      return {
        taskId: task.id,
        slot: this.slot,
        content: '',
        tokens: { input: 0, output: 0 },
        duration: Date.now() - start,
        status: 'failure',
        error: err instanceof Error ? err.message : String(err),
      };
    }
  }

  async *stream(task: OrchestratorTask, signal?: AbortSignal) {
    yield* this.bridge.stream(task.prompt, { signal });
  }
}
