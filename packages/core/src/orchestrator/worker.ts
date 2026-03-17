import type { SlotConfig, SlotName, ThinkingLevel } from '../slots/types.js';
import type { OrchestratorTask, WorkerResult } from './types.js';
import type { CLIBridge, BridgeChunk } from '../bridge/types.js';
import { createBridgeForSlot } from '../bridge/index.js';
import { BridgeError, CircuitBreaker, DEFAULT_RETRY_CONFIG, backoffDelay, type RetryConfig } from '../bridge/errors.js';

export class SlotWorker {
  private bridge: CLIBridge;
  private fallbackBridge?: CLIBridge;
  private slot: SlotName;
  private thinkingLevel?: ThinkingLevel;
  private systemPrompt?: string;
  private circuitBreaker = new CircuitBreaker();
  private retryConfig: RetryConfig = DEFAULT_RETRY_CONFIG;

  constructor(slotName: SlotName, slotConfig: SlotConfig) {
    this.slot = slotName;
    this.bridge = createBridgeForSlot(slotConfig);
    this.thinkingLevel = slotConfig.thinkingLevel;
    this.systemPrompt = slotConfig.systemPrompt;
    if (slotConfig.fallback) {
      this.fallbackBridge = createBridgeForSlot(slotConfig.fallback);
    }
  }

  async execute(task: OrchestratorTask, signal?: AbortSignal): Promise<WorkerResult> {
    const start = Date.now();
    const opts = {
      signal,
      thinkingLevel: this.thinkingLevel,
      systemPrompt: task.systemPrompt ?? this.systemPrompt,
      conversationHistory: task.history,
    };

    // Circuit breaker check
    if (this.circuitBreaker.isOpen()) {
      if (this.fallbackBridge) {
        return this.executeFallback(task, signal, start);
      }
      return {
        taskId: task.id,
        slot: this.slot,
        content: '',
        tokens: { input: 0, output: 0 },
        duration: Date.now() - start,
        status: 'failure',
        error: `Circuit breaker open for slot ${this.slot} after ${this.circuitBreaker.failures} consecutive failures`,
      };
    }

    // Retry loop
    let lastError: Error | null = null;
    for (let attempt = 0; attempt <= this.retryConfig.maxRetries; attempt++) {
      if (signal?.aborted) break;

      try {
        const result = await this.bridge.execute(task.prompt, opts);
        this.circuitBreaker.recordSuccess();
        return {
          taskId: task.id,
          slot: this.slot,
          content: result.content,
          tokens: result.tokens,
          duration: result.duration,
          status: 'success',
        };
      } catch (err) {
        lastError = err instanceof Error ? err : new Error(String(err));
        this.circuitBreaker.recordFailure();

        // Only retry if retryable
        const isRetryable = err instanceof BridgeError ? err.isRetryable() : false;
        if (!isRetryable || attempt === this.retryConfig.maxRetries) break;

        // Wait before retry
        const delay = backoffDelay(attempt, this.retryConfig);
        await new Promise(r => setTimeout(r, delay));
      }
    }

    // Try fallback if available
    if (this.fallbackBridge) {
      return this.executeFallback(task, signal, start);
    }

    return {
      taskId: task.id,
      slot: this.slot,
      content: '',
      tokens: { input: 0, output: 0 },
      duration: Date.now() - start,
      status: 'failure',
      error: lastError?.message ?? 'Unknown error',
    };
  }

  private async executeFallback(task: OrchestratorTask, signal: AbortSignal | undefined, start: number): Promise<WorkerResult> {
    try {
      const result = await this.fallbackBridge!.execute(task.prompt, {
        signal,
        systemPrompt: task.systemPrompt ?? this.systemPrompt,
        conversationHistory: task.history,
      });
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
        error: `Fallback also failed: ${err instanceof Error ? err.message : String(err)}`,
      };
    }
  }

  async *stream(task: OrchestratorTask, signal?: AbortSignal): AsyncGenerator<BridgeChunk> {
    const opts = {
      signal,
      thinkingLevel: this.thinkingLevel,
      systemPrompt: task.systemPrompt ?? this.systemPrompt,
      conversationHistory: task.history,
    };

    // Circuit breaker check — try fallback for streaming too
    if (this.circuitBreaker.isOpen() && this.fallbackBridge) {
      yield* this.fallbackBridge.stream(task.prompt, {
        signal,
        systemPrompt: task.systemPrompt ?? this.systemPrompt,
        conversationHistory: task.history,
      });
      return;
    }

    try {
      yield* this.bridge.stream(task.prompt, opts);
      this.circuitBreaker.recordSuccess();
    } catch (err) {
      this.circuitBreaker.recordFailure();

      // Try fallback on stream failure
      if (this.fallbackBridge) {
        yield { type: 'text', content: '*Switching to fallback model...*\n\n' };
        yield* this.fallbackBridge.stream(task.prompt, {
          signal,
          systemPrompt: task.systemPrompt ?? this.systemPrompt,
          conversationHistory: task.history,
        });
        return;
      }

      yield { type: 'error', content: err instanceof Error ? err.message : String(err) };
      yield { type: 'done', content: '' };
    }
  }
}
