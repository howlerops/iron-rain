/**
 * Skill Executor — runs a skill by injecting its instructions into the system prompt.
 */

import type { EpisodeSummary } from "../episodes/protocol.js";
import type { OrchestratorKernel } from "../orchestrator/kernel.js";
import type { OrchestratorTask } from "../orchestrator/types.js";
import type { Skill } from "./types.js";

export class SkillExecutor {
  private kernel: OrchestratorKernel;

  constructor(kernel: OrchestratorKernel) {
    this.kernel = kernel;
  }

  /**
   * Execute a skill with optional user arguments.
   */
  async execute(skill: Skill, args?: string): Promise<EpisodeSummary> {
    const task = this.buildTask(skill, args);
    return this.kernel.dispatch(task);
  }

  /**
   * Stream a skill execution.
   */
  async *stream(skill: Skill, args?: string, signal?: AbortSignal) {
    const task = this.buildTask(skill, args);
    yield* this.kernel.dispatchStreaming(task, signal);
  }

  private buildTask(skill: Skill, args?: string): OrchestratorTask {
    const prompt = args ? `${args}` : `Execute the ${skill.name} skill.`;

    return {
      id: crypto.randomUUID?.() ?? `skill-${Date.now()}`,
      prompt,
      targetSlot: "main",
      systemPrompt: `${skill.content}\n\n---\nYou are executing the "${skill.name}" skill. Follow the instructions above precisely.`,
    };
  }
}
