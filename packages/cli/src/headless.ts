/**
 * Headless runner — dispatches prompts without TUI.
 * Used for CI, scripting, and piped workflows.
 */

import type { OrchestratorKernel } from "@howlerops/iron-rain";

export type OutputFormat = "text" | "json";

export interface HeadlessEvent {
  type: "text" | "error" | "done";
  content: string;
  slot?: string;
  tokens?: number;
  duration?: number;
}

export interface HeadlessOptions {
  output: OutputFormat;
  timeout?: number;
  noStreaming?: boolean;
}

/**
 * Exit codes for headless mode.
 */
export const EXIT_CODES = {
  SUCCESS: 0,
  TASK_ERROR: 1,
  CONFIG_ERROR: 2,
  PROVIDER_ERROR: 3,
} as const;

let nextId = 1;
function makeId(): string {
  return `headless-${nextId++}`;
}

/**
 * Run a single prompt in headless mode.
 */
export async function runHeadless(
  kernel: OrchestratorKernel,
  prompt: string,
  options: HeadlessOptions,
): Promise<number> {
  const start = Date.now();
  const slot = "main" as const;

  try {
    if (options.noStreaming) {
      const episode = await kernel.dispatch({
        id: makeId(),
        prompt,
        targetSlot: slot,
      });

      const event: HeadlessEvent = {
        type: "done",
        content: episode.result,
        slot: episode.slot,
        tokens: episode.tokens,
        duration: Date.now() - start,
      };

      if (options.output === "json") {
        process.stdout.write(`${JSON.stringify(event)}\n`);
      } else {
        process.stdout.write(`${episode.result}\n`);
      }
      return EXIT_CODES.SUCCESS;
    }

    // Streaming mode
    const stream = kernel.dispatchStreaming({
      id: makeId(),
      prompt,
      targetSlot: slot,
    });
    let fullContent = "";

    for await (const chunk of stream) {
      if (chunk.type === "text") {
        fullContent += chunk.content;
        if (options.output === "json") {
          const event: HeadlessEvent = {
            type: "text",
            content: chunk.content,
            slot: chunk.slot,
          };
          process.stdout.write(`${JSON.stringify(event)}\n`);
        } else {
          process.stdout.write(chunk.content);
        }
      } else if (chunk.type === "error") {
        const event: HeadlessEvent = {
          type: "error",
          content: chunk.content,
          slot: chunk.slot,
        };
        if (options.output === "json") {
          process.stdout.write(`${JSON.stringify(event)}\n`);
        } else {
          process.stderr.write(`Error: ${chunk.content}\n`);
        }
        return EXIT_CODES.TASK_ERROR;
      }
    }

    if (options.output === "json") {
      const event: HeadlessEvent = {
        type: "done",
        content: fullContent,
        slot,
        duration: Date.now() - start,
      };
      process.stdout.write(`${JSON.stringify(event)}\n`);
    }

    return EXIT_CODES.SUCCESS;
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (options.output === "json") {
      const event: HeadlessEvent = { type: "error", content: message };
      process.stdout.write(`${JSON.stringify(event)}\n`);
    } else {
      process.stderr.write(`Error: ${message}\n`);
    }
    return EXIT_CODES.PROVIDER_ERROR;
  }
}

/**
 * Run batch mode — multiple prompts from an array.
 */
export async function runBatch(
  kernel: OrchestratorKernel,
  prompts: string[],
  options: HeadlessOptions,
): Promise<number> {
  const results: HeadlessEvent[] = [];
  let hasError = false;

  for (const prompt of prompts) {
    const start = Date.now();
    try {
      const episode = await kernel.dispatch({
        id: makeId(),
        prompt,
        targetSlot: "main",
      });
      results.push({
        type: "done",
        content: episode.result,
        slot: episode.slot,
        tokens: episode.tokens,
        duration: Date.now() - start,
      });
    } catch (err) {
      hasError = true;
      results.push({
        type: "error",
        content: err instanceof Error ? err.message : String(err),
        duration: Date.now() - start,
      });
    }
  }

  if (options.output === "json") {
    process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
  } else {
    for (const result of results) {
      if (result.type === "error") {
        process.stderr.write(`Error: ${result.content}\n`);
      } else {
        process.stdout.write(`${result.content}\n---\n`);
      }
    }
  }

  return hasError ? EXIT_CODES.TASK_ERROR : EXIT_CODES.SUCCESS;
}
