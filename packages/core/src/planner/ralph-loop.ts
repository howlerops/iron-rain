/**
 * Ralph Wiggum Loop — iterative task execution until a completion condition is met.
 */

import { autoCommit } from "../git/utils.js";
import type { OrchestratorKernel } from "../orchestrator/kernel.js";
import {
  buildCompletionCheckPrompt,
  buildLoopIterationPrompt,
} from "./prompts.js";
import type { PlanStorage } from "./storage.js";
import type {
  LoopCallbacks,
  LoopConfig,
  LoopIteration,
  LoopState,
} from "./types.js";

export class RalphLoop {
  private kernel: OrchestratorKernel;
  private callbacks: LoopCallbacks;
  private paused = false;

  constructor(
    kernel: OrchestratorKernel,
    callbacks: LoopCallbacks,
    _storage?: PlanStorage,
  ) {
    this.kernel = kernel;
    this.callbacks = callbacks;
  }

  async run(config: LoopConfig, signal?: AbortSignal): Promise<LoopState> {
    const state: LoopState = {
      id: crypto.randomUUID?.() ?? `loop-${Date.now()}`,
      config,
      iterations: [],
      status: "running",
      createdAt: Date.now(),
      updatedAt: Date.now(),
    };

    this.paused = false;

    for (let i = 0; i < config.maxIterations; i++) {
      if (signal?.aborted || this.paused) {
        state.status = "paused";
        break;
      }

      this.callbacks.onIterationStart?.(i);
      const start = Date.now();

      try {
        // Execute iteration
        const prompt = buildLoopIterationPrompt({
          want: config.want,
          completionPromise: config.completionPromise,
          iterationIndex: i,
          maxIterations: config.maxIterations,
          priorActions: state.iterations.map((it) => it.action),
        });

        const iterTask = {
          id: `loop-${state.id}-iter-${i}`,
          prompt,
          targetSlot: "execute" as const,
          systemPrompt: `You are Forge, working iteratively to achieve a goal. This is iteration ${i + 1} of ${config.maxIterations}.`,
        };

        const episode = this.callbacks.dispatchFn
          ? await this.callbacks.dispatchFn(iterTask)
          : await this.kernel.dispatch(iterTask);

        const duration = Date.now() - start;

        // Auto-commit if enabled
        let commitHash: string | undefined;
        if (config.autoCommit) {
          commitHash = autoCommit(
            `loop iteration ${i + 1}: ${config.want.slice(0, 50)}`,
          );
        }

        // Check completion
        const completionMet = await this.checkCompletion(
          config.completionPromise,
          episode.result,
        );

        const iteration: LoopIteration = {
          index: i,
          action: episode.result.slice(0, 500),
          result: episode.result,
          completionMet,
          commitHash,
          duration,
          tokens: episode.tokens,
        };

        state.iterations.push(iteration);
        state.updatedAt = Date.now();
        this.callbacks.onIterationComplete?.(iteration);

        if (completionMet) {
          state.status = "completed";
          break;
        }
      } catch (err) {
        const iteration: LoopIteration = {
          index: i,
          action: `Error: ${err instanceof Error ? err.message : String(err)}`,
          result: "",
          completionMet: false,
          duration: Date.now() - start,
          tokens: 0,
        };
        state.iterations.push(iteration);
        state.updatedAt = Date.now();
      }
    }

    if (state.status === "running") {
      state.status = "failed"; // Max iterations reached without completion
    }

    state.updatedAt = Date.now();
    this.callbacks.onComplete?.(state);
    return state;
  }

  pause(): void {
    this.paused = true;
  }

  async resume(state: LoopState, signal?: AbortSignal): Promise<LoopState> {
    const remaining = state.config.maxIterations - state.iterations.length;
    if (remaining <= 0) {
      state.status = "failed";
      return state;
    }

    const _resumeConfig: LoopConfig = {
      ...state.config,
      maxIterations: remaining,
    };

    // Create a new loop with existing iterations as context
    this.paused = false;
    state.status = "running";

    for (let i = state.iterations.length; i < state.config.maxIterations; i++) {
      if (signal?.aborted || this.paused) {
        state.status = "paused";
        break;
      }

      this.callbacks.onIterationStart?.(i);
      const start = Date.now();

      const prompt = buildLoopIterationPrompt({
        want: state.config.want,
        completionPromise: state.config.completionPromise,
        iterationIndex: i,
        maxIterations: state.config.maxIterations,
        priorActions: state.iterations.map((it) => it.action),
      });

      try {
        const resumeTask = {
          id: `loop-${state.id}-iter-${i}`,
          prompt,
          targetSlot: "execute" as const,
        };

        const episode = this.callbacks.dispatchFn
          ? await this.callbacks.dispatchFn(resumeTask)
          : await this.kernel.dispatch(resumeTask);

        const duration = Date.now() - start;
        let commitHash: string | undefined;
        if (state.config.autoCommit) {
          commitHash = autoCommit(`loop iteration ${i + 1}`);
        }

        const completionMet = await this.checkCompletion(
          state.config.completionPromise,
          episode.result,
        );

        const iteration: LoopIteration = {
          index: i,
          action: episode.result.slice(0, 500),
          result: episode.result,
          completionMet,
          commitHash,
          duration,
          tokens: episode.tokens,
        };

        state.iterations.push(iteration);
        state.updatedAt = Date.now();
        this.callbacks.onIterationComplete?.(iteration);

        if (completionMet) {
          state.status = "completed";
          break;
        }
      } catch {
        break;
      }
    }

    if (state.status === "running") state.status = "failed";
    state.updatedAt = Date.now();
    this.callbacks.onComplete?.(state);
    return state;
  }

  private async checkCompletion(
    promise: string,
    lastResult: string,
  ): Promise<boolean> {
    try {
      const prompt = buildCompletionCheckPrompt(promise, lastResult);
      const episode = await this.kernel.dispatch({
        id: `completion-check-${Date.now()}`,
        prompt,
        targetSlot: "main",
      });
      return episode.result.toUpperCase().startsWith("TRUE");
    } catch {
      return false;
    }
  }
}
