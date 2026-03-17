/**
 * Plan Executor — sequentially executes plan tasks through the execute slot.
 */
import { execSync } from 'node:child_process';
import type { OrchestratorKernel } from '../orchestrator/kernel.js';
import type { Plan, PlanTask, PlanCallbacks } from './types.js';
import { buildTaskExecutionPrompt } from './prompts.js';
import { PlanStorage } from './storage.js';

export class PlanExecutor {
  private kernel: OrchestratorKernel;
  private storage = new PlanStorage();
  private paused = false;
  private currentAbort: AbortController | null = null;

  constructor(kernel: OrchestratorKernel) {
    this.kernel = kernel;
  }

  /**
   * Execute all pending tasks in a plan sequentially.
   */
  async executePlan(plan: Plan, callbacks?: PlanCallbacks, signal?: AbortSignal): Promise<Plan> {
    plan.status = 'executing';
    plan.updatedAt = Date.now();
    this.storage.save(plan);
    this.paused = false;

    const priorResults: string[] = [];

    for (const task of plan.tasks) {
      if (signal?.aborted || this.paused) {
        plan.status = 'paused';
        break;
      }

      if (task.status === 'completed' || task.status === 'skipped') {
        if (task.result) priorResults.push(task.result.output);
        continue;
      }

      task.status = 'in_progress';
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
          targetSlot: 'execute',
          systemPrompt: `You are Forge, executing task ${task.index + 1} of ${plan.tasks.length}: "${task.title}". Implement it completely.`,
        });

        const duration = Date.now() - start;

        task.status = episode.status === 'failure' ? 'failed' : 'completed';
        task.result = {
          output: episode.result,
          filesModified: episode.filesModified ?? [],
          duration,
          tokens: episode.tokens,
        };

        if (task.status === 'completed') {
          plan.stats.tasksCompleted++;
          priorResults.push(episode.result);

          // Auto-commit if enabled
          if (plan.autoCommit) {
            const commitHash = this.autoCommit(task.title);
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
        task.status = 'failed';
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
      plan.status = plan.stats.tasksFailed > 0 ? 'failed' : 'completed';
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

  private autoCommit(taskTitle: string): string | undefined {
    try {
      execSync('git add -A', { stdio: 'pipe' });
      const msg = `iron-rain: ${taskTitle}`;
      execSync(`git commit -m ${JSON.stringify(msg)} --allow-empty`, { stdio: 'pipe' });
      const hash = execSync('git rev-parse --short HEAD', { stdio: 'pipe' }).toString().trim();
      return hash;
    } catch {
      return undefined;
    }
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
    signal.addEventListener('abort', () => controller.abort(signal.reason), { once: true });
  }
  return controller.signal;
}
