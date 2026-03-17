import type { LoopConfig, LoopState } from "@howlerops/iron-rain";
import { RalphLoop } from "@howlerops/iron-rain";
import type { SessionControllerContext } from "./context.js";

export class LoopController {
  constructor(private readonly ctx: SessionControllerContext) {}

  async handleCommand(text: string): Promise<boolean> {
    if (text.startsWith("/loop ")) {
      const rest = text.slice(6).trim();
      const untilMatch = rest.match(/(.+?)\s+--until\s+"([^"]+)"/);
      if (untilMatch) {
        await this.startLoop(untilMatch[1].trim(), untilMatch[2]);
      } else {
        this.ctx.addSystemMessage(
          'Usage: /loop <description> --until "<condition>"',
        );
      }
      return true;
    }

    if (text === "/loop") {
      this.ctx.addSystemMessage(
        'Usage: /loop <description> --until "<condition>"',
      );
      return true;
    }

    if (text === "/loop-status") {
      const loop = this.ctx.actions.activeLoop();
      if (loop) {
        const lines = [
          `**Goal:** ${loop.config.want}`,
          `**Condition:** ${loop.config.completionPromise}`,
          `**Status:** ${loop.status}`,
          `**Iterations:** ${loop.iterations.length}/${loop.config.maxIterations}`,
        ];
        if (loop.iterations.length > 0) {
          const last = loop.iterations[loop.iterations.length - 1];
          lines.push(`**Last action:** ${last.action.slice(0, 200)}`);
        }
        this.ctx.addSystemMessage(lines.join("\n"));
      } else {
        this.ctx.addSystemMessage(
          'No active loop. Use `/loop <description> --until "<condition>"` to start one.',
        );
      }
      return true;
    }

    if (text === "/loop-pause") {
      this.ctx.addSystemMessage(
        "Loop pause requested. The loop will stop after the current iteration.",
      );
      return true;
    }

    if (text === "/loop-resume") {
      const loop = this.ctx.actions.activeLoop();
      if (loop && loop.status === "paused") {
        await this.resumeLoop(loop);
      } else {
        this.ctx.addSystemMessage("No paused loop to resume.");
      }
      return true;
    }

    return false;
  }

  async startLoop(want: string, until: string) {
    this.ctx.setMode("loop-running");

    const config: LoopConfig = {
      want,
      completionPromise: until,
      maxIterations: 10,
      autoCommit: true,
    };

    this.ctx.addSystemMessage(
      `Starting loop: *${want}*\nUntil: "${until}"\nMax iterations: ${config.maxIterations}`,
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
          `**Iteration ${iter.index + 1}** — ${status}${iter.commitHash ? ` (${iter.commitHash})` : ""}`,
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
        `Loop error: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      this.ctx.setMode("chat");
    }
  }

  async resumeLoop(loopState: LoopState) {
    this.ctx.setMode("loop-running");
    this.ctx.addSystemMessage("Resuming loop...");

    const kernel = this.ctx.actions
      .getDispatcher()
      .ensureKernel(this.ctx.state.slots);
    const loop = new RalphLoop(kernel, {
      onIterationComplete: (iter) => {
        this.ctx.addSystemMessage(
          `**Iteration ${iter.index + 1}** — ${iter.completionMet ? "CONDITION MET" : "continuing"}`,
        );
      },
      onComplete: (state) => {
        this.ctx.addSystemMessage(
          `Loop ${state.status} after ${state.iterations.length} iterations.`,
        );
      },
    });

    try {
      const result = await loop.resume(loopState);
      this.ctx.actions.setActiveLoop(result);
    } catch (err) {
      this.ctx.addSystemMessage(
        `Loop resume error: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      this.ctx.setMode("chat");
    }
  }
}
