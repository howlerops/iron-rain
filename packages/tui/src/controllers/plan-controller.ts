import type { Plan } from "@howlerops/iron-rain";
import { PlanExecutor, PlanGenerator, PlanStorage } from "@howlerops/iron-rain";
import type { SessionControllerContext } from "./context.js";

export class PlanController {
  constructor(private readonly ctx: SessionControllerContext) {}

  async handleCommand(text: string): Promise<boolean> {
    if (text.startsWith("/plan ")) {
      const want = text.slice(6).trim();
      if (!want) {
        this.ctx.addSystemMessage(
          "Usage: /plan <description of what you want to build>",
        );
        return true;
      }
      await this.generatePlan(want);
      return true;
    }

    if (text === "/plan") {
      this.ctx.addSystemMessage(
        "Usage: /plan <description of what you want to build>",
      );
      return true;
    }

    if (text === "/plans") {
      const storage = new PlanStorage();
      const plans = storage.list();
      if (plans.length === 0) {
        this.ctx.addSystemMessage(
          "No saved plans. Use `/plan <description>` to create one.",
        );
      } else {
        const lines = plans.map(
          (p) =>
            `- **${p.title}** (${p.status}) — ${new Date(p.createdAt).toLocaleDateString()}`,
        );
        this.ctx.addSystemMessage(`## Saved Plans\n${lines.join("\n")}`);
      }
      return true;
    }

    if (text === "/resume") {
      const plan = this.ctx.actions.activePlan();
      if (plan && (plan.status === "paused" || plan.status === "approved")) {
        await this.executePlan(plan);
      } else {
        this.ctx.addSystemMessage(
          "No paused plan to resume. Use `/plans` to see saved plans.",
        );
      }
      return true;
    }

    return false;
  }

  async generatePlan(want: string) {
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

      this.ctx.setMode("plan-review");
      this.ctx.addSystemMessage(
        `Plan generated with ${plan.tasks.length} tasks. Review below.\n\nType **approve**, **reject**, or provide feedback.`,
      );
    } catch (err) {
      this.ctx.addSystemMessage(
        `Plan generation failed: ${err instanceof Error ? err.message : String(err)}`,
      );
      this.ctx.setMode("chat");
    }
  }

  async executePlan(plan: Plan) {
    this.ctx.setMode("plan-executing");
    plan.status = "approved";
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
            `Failed task ${task.index + 1}: **${task.title}** — ${error}`,
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
}
