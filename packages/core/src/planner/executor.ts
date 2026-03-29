/**
 * Plan Executor — executes plan tasks through the execute slot.
 * Independent tasks (no unmet dependencies) run in parallel.
 */

import type { EpisodeSummary } from "../episodes/protocol.js";
import { autoCommit } from "../git/utils.js";
import type { OrchestratorKernel } from "../orchestrator/kernel.js";
import { buildTaskExecutionPrompt } from "./prompts.js";
import { PlanStorage } from "./storage.js";
import type { Plan, PlanCallbacks, PlanTask } from "./types.js";

export class PlanExecutor {
  private kernel: OrchestratorKernel;
  private storage: PlanStorage;
  private paused = false;
  private currentAborts: AbortController[] = [];

  constructor(kernel: OrchestratorKernel, storage?: PlanStorage) {
    this.kernel = kernel;
    this.storage = storage ?? new PlanStorage();
  }

  /**
   * Execute all pending tasks in a plan.
   * Independent tasks (no unmet dependsOn) run concurrently in waves.
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

    const completedIds = new Set<string>();
    const resultMap = new Map<string, string>();
    const episodeMap = new Map<string, EpisodeSummary>();

    // Pre-populate completed tasks
    for (const task of plan.tasks) {
      if (task.status === "completed" || task.status === "skipped") {
        completedIds.add(task.id);
        if (task.result) resultMap.set(task.id, task.result.output);
      }
    }

    // Execute in waves until all tasks are done
    while (!signal?.aborted && !this.paused) {
      // Find ready tasks: pending + all dependencies met
      const ready = plan.tasks.filter((t) => {
        if (t.status !== "pending") return false;
        const deps = t.dependsOn ?? [];
        return deps.every((dep) => completedIds.has(dep));
      });

      if (ready.length === 0) break; // No more tasks to run

      // Build prior results for context
      const priorResults = [...resultMap.values()];

      // Execute wave — all ready tasks in parallel
      // Pass dependency episodes as inputEpisodes for thread composition
      const wavePromises = ready.map((task) => {
        const depEpisodes = (task.dependsOn ?? [])
          .map((dep) => episodeMap.get(dep))
          .filter((ep): ep is EpisodeSummary => ep != null);
        return this.executeTask(
          task,
          plan,
          priorResults,
          depEpisodes,
          signal,
          callbacks,
        );
      });

      const waveResults = await Promise.allSettled(wavePromises);

      // Process results
      for (let i = 0; i < ready.length; i++) {
        const task = ready[i];
        const result = waveResults[i];

        if (result.status === "fulfilled" && task.status === "completed") {
          completedIds.add(task.id);
          if (task.result) resultMap.set(task.id, task.result.output);
          // Store episode for downstream thread composition
          if (result.value) episodeMap.set(task.id, result.value);
        }
      }

      plan.updatedAt = Date.now();
      this.storage.save(plan);
    }

    if (this.paused || signal?.aborted) {
      plan.status = "paused";
    } else {
      plan.status = plan.stats.tasksFailed > 0 ? "failed" : "completed";
    }

    plan.updatedAt = Date.now();
    this.storage.save(plan);
    callbacks?.onPlanComplete?.(plan);

    return plan;
  }

  private async executeTask(
    task: PlanTask,
    plan: Plan,
    priorResults: string[],
    depEpisodes: EpisodeSummary[],
    signal?: AbortSignal,
    callbacks?: PlanCallbacks,
  ): Promise<EpisodeSummary | undefined> {
    task.status = "in_progress";
    callbacks?.onTaskStart?.(task);

    const start = Date.now();
    const abort = new AbortController();
    this.currentAborts.push(abort);

    try {
      const _taskSignal = signal
        ? anySignal(signal, abort.signal)
        : abort.signal;

      const prompt = buildTaskExecutionPrompt(task, plan.prd, priorResults);

      const dispatchTask = {
        id: task.id,
        prompt,
        targetSlot: "execute" as const,
        systemPrompt: `You are Forge, executing task ${task.index + 1} of ${plan.tasks.length}: "${task.title}". Implement it completely. Use parallel tool calls for independent operations.`,
        // Thread composition: pass dependency episodes as direct context
        inputEpisodes: depEpisodes.length > 0 ? depEpisodes : undefined,
      };

      const episode = callbacks?.dispatchFn
        ? await callbacks.dispatchFn(dispatchTask)
        : await this.kernel.dispatch(dispatchTask);

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

      return episode;
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
      return undefined;
    } finally {
      const idx = this.currentAborts.indexOf(abort);
      if (idx >= 0) this.currentAborts.splice(idx, 1);
    }
  }

  pause(): void {
    this.paused = true;
    for (const abort of this.currentAborts) {
      abort.abort();
    }
    this.currentAborts = [];
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
