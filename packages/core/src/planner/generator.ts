/**
 * Plan Generator — generates PRD + task breakdown via Cortex (main slot).
 */
import type { OrchestratorKernel } from "../orchestrator/kernel.js";
import { PRD_SYSTEM_PROMPT, TASK_BREAKDOWN_PROMPT } from "./prompts.js";
import type { Plan, PlanTask } from "./types.js";

export class PlanGenerator {
  private kernel: OrchestratorKernel;

  constructor(kernel: OrchestratorKernel) {
    this.kernel = kernel;
  }

  /**
   * Generate a full plan (PRD + tasks) from a user's "want" description.
   */
  async generatePlan(want: string, codebaseContext?: string): Promise<Plan> {
    const planId = crypto.randomUUID?.() ?? `plan-${Date.now()}`;

    // Step 1: Generate PRD
    const prdPrompt = codebaseContext
      ? `${want}\n\n## Codebase Context\n${codebaseContext}`
      : want;

    const prdEpisode = await this.kernel.dispatch({
      id: `${planId}-prd`,
      prompt: prdPrompt,
      targetSlot: "main",
      systemPrompt: PRD_SYSTEM_PROMPT,
    });

    const prd = prdEpisode.result || "";

    // Step 2: Break down into tasks
    const taskEpisode = await this.kernel.dispatch({
      id: `${planId}-tasks`,
      prompt: `${TASK_BREAKDOWN_PROMPT}\n\n## PRD\n${prd}`,
      targetSlot: "main",
    });

    const tasks = this.parseTasks(taskEpisode.result || "[]", planId);

    // Extract title from PRD (first heading or first line)
    const titleMatch = prd.match(/^#\s+(.+)$/m);
    const title = titleMatch?.[1] ?? want.slice(0, 80);

    return {
      id: planId,
      title,
      description: want,
      prd,
      tasks,
      status: "review",
      autoCommit: true,
      createdAt: Date.now(),
      updatedAt: Date.now(),
      stats: {
        tasksCompleted: 0,
        tasksFailed: 0,
        totalDuration: 0,
        totalTokens: 0,
      },
    };
  }

  /**
   * Stream PRD generation for live display.
   */
  async *streamPRD(want: string, signal?: AbortSignal) {
    yield* this.kernel.dispatchStreaming(
      {
        id: `prd-stream-${Date.now()}`,
        prompt: want,
        targetSlot: "main",
        systemPrompt: PRD_SYSTEM_PROMPT,
      },
      signal,
    );
  }

  private parseTasks(raw: string, planId: string): PlanTask[] {
    // Extract JSON array from response (may be wrapped in markdown code block)
    const jsonMatch = raw.match(/\[[\s\S]*\]/);
    if (!jsonMatch) return [];

    try {
      const parsed = JSON.parse(jsonMatch[0]) as Array<{
        title: string;
        description: string;
        acceptanceCriteria?: string[];
        targetFiles?: string[];
        dependsOn?: number[];
      }>;

      return parsed.map((t, i) => ({
        id: `${planId}-task-${i}`,
        index: i,
        title: t.title,
        description: t.description,
        acceptanceCriteria: t.acceptanceCriteria ?? [],
        status: "pending" as const,
        targetFiles: t.targetFiles,
        dependsOn: t.dependsOn?.map((dep) => `${planId}-task-${dep}`),
      }));
    } catch {
      return [];
    }
  }
}
