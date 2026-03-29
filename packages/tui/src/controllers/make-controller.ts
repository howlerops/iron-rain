import type {
  LoopConfig,
  LoopState,
  Plan,
  PlanTask,
} from "@howlerops/iron-rain";
import {
  PlanExecutor,
  PlanGenerator,
  PlanStorage,
  RalphLoop,
} from "@howlerops/iron-rain";
import type { MakeWizardOptions } from "../components/make-wizard/types.js";
import { formatDuration, formatTokens } from "../components/session-view.js";
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
        dispatchFn: (task) => this.ctx.actions.dispatchForTask(task),
        onTaskStart: (task) => {
          this.ctx.addSystemMessage(
            `Starting task ${task.index + 1}: **${task.title}**`,
          );
        },
        onTaskComplete: (task) => {
          const stats = this.formatTaskStats(task);
          this.ctx.addSystemMessage(
            `Completed task ${task.index + 1}: **${task.title}**${stats}${task.result?.commitHash ? ` (${task.result.commitHash})` : ""}`,
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
          this.ctx.addSystemMessage(
            this.buildPlanCompletionMessage(completedPlan),
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
      dispatchFn: (task) => this.ctx.actions.dispatchForTask(task),
      onIterationStart: (i) => {
        this.ctx.addSystemMessage(`**Iteration ${i + 1}** starting...`);
      },
      onIterationComplete: (iter) => {
        const status = iter.completionMet ? "CONDITION MET" : "continuing";
        const stats = ` (${formatDuration(iter.duration)} · ${formatTokens(iter.tokens)} tokens)`;
        this.ctx.addSystemMessage(
          `**Iteration ${iter.index + 1}** \u2014 ${status}${stats}${iter.commitHash ? ` (${iter.commitHash})` : ""}`,
        );
      },
      onComplete: (loopState) => {
        this.ctx.addSystemMessage(this.buildLoopCompletionMessage(loopState));
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

  private formatTaskStats(task: PlanTask): string {
    if (!task.result) return "";
    const parts: string[] = [];
    if (task.result.duration > 0)
      parts.push(formatDuration(task.result.duration));
    if (task.result.filesModified.length > 0)
      parts.push(`${task.result.filesModified.length} files`);
    if (task.result.tokens > 0)
      parts.push(`${formatTokens(task.result.tokens)} tokens`);
    return parts.length > 0 ? ` (${parts.join(" \u00B7 ")})` : "";
  }

  private buildPlanCompletionMessage(plan: Plan): string {
    const status =
      plan.status === "completed" ? "Plan Complete" : `Plan ${plan.status}`;
    const header = `**${status}: ${plan.title}**`;
    const summary = `${plan.stats.tasksCompleted}/${plan.tasks.length} tasks \u00B7 ${formatDuration(plan.stats.totalDuration)} \u00B7 ${formatTokens(plan.stats.totalTokens)} tokens`;

    const rows = plan.tasks
      .filter((t) => t.result)
      .map((t) => {
        const d = formatDuration(t.result!.duration);
        const f = `${t.result!.filesModified.length}`;
        const tk = formatTokens(t.result!.tokens);
        return `| ${t.index + 1}. ${t.title} | ${d} | ${f} | ${tk} |`;
      });

    const commits = plan.tasks.map((t) => t.result?.commitHash).filter(Boolean);

    let msg = `${header}\n${summary}`;

    if (rows.length > 0) {
      msg += `\n\n| Task | Duration | Files | Tokens |\n|------|----------|-------|--------|\n${rows.join("\n")}`;
    }

    if (commits.length > 0) {
      msg += `\n\nCommits: ${commits.join(", ")}`;
    }

    return msg;
  }

  private buildLoopCompletionMessage(loopState: LoopState): string {
    const status =
      loopState.status === "completed"
        ? "Loop Complete"
        : `Loop ${loopState.status}`;
    const totalDuration = loopState.iterations.reduce(
      (sum, it) => sum + it.duration,
      0,
    );
    const totalTokens = loopState.iterations.reduce(
      (sum, it) => sum + it.tokens,
      0,
    );
    const commits = loopState.iterations
      .map((it) => it.commitHash)
      .filter(Boolean);

    let msg = `**${status}**\n${loopState.iterations.length} iterations \u00B7 ${formatDuration(totalDuration)} \u00B7 ${formatTokens(totalTokens)} tokens`;

    if (commits.length > 0) {
      msg += `\n\nCommits: ${commits.join(", ")}`;
    }

    return msg;
  }
}
