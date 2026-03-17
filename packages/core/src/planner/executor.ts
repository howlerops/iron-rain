/**
 * Plan Executor — sequentially executes plan tasks through the execute slot.
 */

import { autoCommit } from "../git/utils.js";
import type { OrchestratorKernel } from "../orchestrator/kernel.js";
import { buildTaskExecutionPrompt } from "./prompts.js";
import { PlanStorage } from "./storage.js";
import type { Plan, PlanCallbacks, PlanTask } from "./types.js";

export class PlanExecutor {
  private kernel: OrchestratorKernel;
  private storage: PlanStorage;
  private paused = false;
  private currentAbort: AbortController | null = null;

  constructor(kernel: OrchestratorKernel, storage?: PlanStorage) {
    this.kernel = kernel;
    this.storage = storage ?? new PlanStorage();
  }

  /**
   * Execute all pending tasks in a plan sequentially.
   */
  async executePlan(
    plan: Plan,
    callbacks?: PlanCallbacks,
    signal?: AbortSignal,
  ): Promise<Plan> {
    plan.status = "executing";
    plan.updatedAt = Date.now();
    this.storage.save(plan);
    this.paused = false;

    const priorResults: string[] = [];

    for (const task of plan.tasks) {
      if (signal?.aborted || this.paused) {
        plan.status = "paused";
        break;
      }

      if (task.status === "completed" || task.status === "skipped") {
        if (task.result) priorResults.push(task.result.output);
        continue;
      }

      task.status = "in_progress";
      callbacks?.onTaskStart?.(task);

      const start = Date.now();

      try {
        this.currentAbort = new AbortController();
        const taskSignal = signal
          ? anySignal(signal, this.currentAbort.signal)
          : this.currentAbort.signal;

        const prompt = buildTaskExecutionPrompt(task, plan.prd, priorResults);

        const episode = await this.kernel.dispatch({
          id: task.id,
          prompt,
          targetSlot: "execute",
          systemPrompt: `You are Forge, executing task ${task.index + 1} of ${plan.tasks.length}: "${task.title}". Implement it completely.`,
        });

        const duration = Date.now() - start;

        task.status = episode.status === "failure" ? "failed" : "completed";
        task.result = {
          output: episode.result,
          filesModified: episode.filesModified ?? [],
          duration,
          tokens: episode.tokens,
        };

        if (task.status === "completed") {
          plan.stats.tasksCompleted++;
          priorResults.push(episode.result);

          // Auto-commit if enabled
          if (plan.autoCommit) {
            const commitHash = autoCommit(task.title);
            if (commitHash) task.result.commitHash = commitHash;
          }

          callbacks?.onTaskComplete?.(task);
        } else {
          plan.stats.tasksFailed++;
          callbacks?.onTaskFail?.(task, episode.result);
        }

        plan.stats.totalDuration += duration;
        plan.stats.totalTokens += episode.tokens;
      } catch (err) {
        task.status = "failed";
        task.result = {
          output: err instanceof Error ? err.message : String(err),
          filesModified: [],
          duration: Date.now() - start,
          tokens: 0,
        };
        plan.stats.tasksFailed++;
        callbacks?.onTaskFail?.(task, task.result.output);
      } finally {
        this.currentAbort = null;
      }

      plan.updatedAt = Date.now();
      this.storage.save(plan);
    }

    if (!this.paused && !signal?.aborted) {
      plan.status = plan.stats.tasksFailed > 0 ? "failed" : "completed";
    }

    plan.updatedAt = Date.now();
    this.storage.save(plan);
    callbacks?.onPlanComplete?.(plan);

    return plan;
  }

  pause(): void {
    this.paused = true;
    this.currentAbort?.abort();
  }
}

/**
 * Create a signal that aborts when any of the input signals abort.
 */
function anySignal(...signals: AbortSignal[]): AbortSignal {
  const controller = new AbortController();
  for (const signal of signals) {
    if (signal.aborted) {
      controller.abort(signal.reason);
      break;
    }
    signal.addEventListener("abort", () => controller.abort(signal.reason), {
      once: true,
    });
  }
  return controller.signal;
}
