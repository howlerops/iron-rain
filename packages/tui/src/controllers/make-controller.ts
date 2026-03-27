import type { LoopConfig, Plan } from "@howlerops/iron-rain";
import {
  PlanExecutor,
  PlanGenerator,
  PlanStorage,
  RalphLoop,
} from "@howlerops/iron-rain";
import type { MakeWizardOptions } from "../components/make-wizard/types.js";
import type { SessionControllerContext } from "./context.js";

export class MakeController {
  constructor(private readonly ctx: SessionControllerContext) {}

  async handleCommand(text: string): Promise<boolean> {
    if (text.startsWith("/make ")) {
      const want = text.slice(6).trim();
      if (!want) {
        this.ctx.addSystemMessage(
          "Usage: /make <description of what you want to build>",
        );
        return true;
      }
      await this.startMake(want);
      return true;
    }

    if (text === "/make") {
      this.ctx.addSystemMessage(
        "Usage: /make <description of what you want to build>",
      );
      return true;
    }

    return false;
  }

  async startMake(want: string) {
    this.ctx.setMode("plan-generating");
    this.ctx.addSystemMessage(`Generating plan for: *${want}*`);

    const kernel = this.ctx.actions
      .getDispatcher()
      .ensureKernel(this.ctx.state.slots);
    const generator = new PlanGenerator(kernel);

    try {
      const plan = await generator.generatePlan(want);
      this.ctx.actions.setActivePlan(plan);

      const storage = new PlanStorage();
      storage.save(plan);

      this.ctx.setMode("make-wizard");
    } catch (err) {
      this.ctx.addSystemMessage(
        `Plan generation failed: ${err instanceof Error ? err.message : String(err)}`,
      );
      this.ctx.setMode("chat");
    }
  }

  async regeneratePlan(plan: Plan, feedback: string) {
    this.ctx.setMode("plan-generating");
    this.ctx.addSystemMessage(`Regenerating plan with feedback: *${feedback}*`);

    const kernel = this.ctx.actions
      .getDispatcher()
      .ensureKernel(this.ctx.state.slots);
    const generator = new PlanGenerator(kernel);

    try {
      const newPlan = await generator.generatePlan(
        `${plan.description}\n\nFeedback: ${feedback}`,
      );
      this.ctx.actions.setActivePlan(newPlan);

      const storage = new PlanStorage();
      storage.save(newPlan);

      this.ctx.setMode("make-wizard");
    } catch (err) {
      this.ctx.addSystemMessage(
        `Plan regeneration failed: ${err instanceof Error ? err.message : String(err)}`,
      );
      this.ctx.setMode("chat");
    }
  }

  async executePlan(plan: Plan, options: MakeWizardOptions) {
    if (options.useLoop) {
      await this.executeWithLoop(plan, options);
    } else {
      await this.executeWithPlanExecutor(plan, options);
    }
  }

  private async executeWithPlanExecutor(
    plan: Plan,
    options: MakeWizardOptions,
  ) {
    this.ctx.setMode("plan-executing");
    plan.status = "approved";
    plan.autoCommit = options.autoCommit;
    this.ctx.actions.setActivePlan(plan);

    const kernel = this.ctx.actions
      .getDispatcher()
      .ensureKernel(this.ctx.state.slots);
    const executor = new PlanExecutor(kernel);

    try {
      const result = await executor.executePlan(plan, {
        onTaskStart: (task) => {
          this.ctx.addSystemMessage(
            `Starting task ${task.index + 1}: **${task.title}**`,
          );
        },
        onTaskComplete: (task) => {
          this.ctx.addSystemMessage(
            `Completed task ${task.index + 1}: **${task.title}**${task.result?.commitHash ? ` (${task.result.commitHash})` : ""}`,
          );
          this.ctx.actions.setActivePlan({ ...plan });
        },
        onTaskFail: (task, error) => {
          this.ctx.addSystemMessage(
            `Failed task ${task.index + 1}: **${task.title}** \u2014 ${error}`,
          );
          this.ctx.actions.setActivePlan({ ...plan });
        },
        onPlanComplete: (completedPlan) => {
          const status =
            completedPlan.status === "completed"
              ? "completed successfully"
              : `finished with status: ${completedPlan.status}`;
          this.ctx.addSystemMessage(
            `Plan ${status}. ${completedPlan.stats.tasksCompleted}/${completedPlan.tasks.length} tasks completed.`,
          );
        },
      });

      this.ctx.actions.setActivePlan(result);
    } catch (err) {
      this.ctx.addSystemMessage(
        `Plan execution error: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      this.ctx.setMode("chat");
    }
  }

  private async executeWithLoop(plan: Plan, options: MakeWizardOptions) {
    this.ctx.setMode("loop-running");

    const taskDescriptions = plan.tasks
      .map((t) => `${t.index + 1}. ${t.title}: ${t.description}`)
      .join("\n");

    const config: LoopConfig = {
      want: `${plan.title}\n\nTasks:\n${taskDescriptions}${options.notes ? `\n\nNotes: ${options.notes}` : ""}`,
      completionPromise: `All ${plan.tasks.length} tasks from the plan are completed`,
      maxIterations: options.maxIterations,
      autoCommit: options.autoCommit,
    };

    this.ctx.addSystemMessage(
      `Starting loop execution: *${plan.title}*\nMax iterations: ${config.maxIterations}`,
    );

    const kernel = this.ctx.actions
      .getDispatcher()
      .ensureKernel(this.ctx.state.slots);
    const loop = new RalphLoop(kernel, {
      onIterationStart: (i) => {
        this.ctx.addSystemMessage(`**Iteration ${i + 1}** starting...`);
      },
      onIterationComplete: (iter) => {
        const status = iter.completionMet ? "CONDITION MET" : "continuing";
        this.ctx.addSystemMessage(
          `**Iteration ${iter.index + 1}** \u2014 ${status}${iter.commitHash ? ` (${iter.commitHash})` : ""}`,
        );
      },
      onComplete: (loopState) => {
        const msg =
          loopState.status === "completed"
            ? `Loop completed after ${loopState.iterations.length} iterations.`
            : `Loop ${loopState.status} after ${loopState.iterations.length} iterations.`;
        this.ctx.addSystemMessage(msg);
      },
    });

    try {
      const result = await loop.run(config);
      this.ctx.actions.setActiveLoop(result);
    } catch (err) {
      this.ctx.addSystemMessage(
        `Loop execution error: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      this.ctx.setMode("chat");
    }
  }
}
