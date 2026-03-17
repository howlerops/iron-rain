import { spawn } from "node:child_process";
import { BaseCLIBridge } from "./base-cli.js";
import { BridgeError } from "./errors.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";

export class GeminiCLIBridge extends BaseCLIBridge {
  constructor(opts: { model?: string; binaryPath?: string }) {
    super({
      name: "gemini-cli",
      model: opts.model ?? "gemini-2.5-pro",
      binaryPath: opts.binaryPath ?? "gemini",
    });
  }

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const args = [
      "-p",
      prompt,
      "--output-format",
      "json",
      "--model",
      this.model,
    ];

    try {
      const { stdout, stderr, exitCode } = await this.spawnAndCollect(
        args,
        options?.signal,
      );
      const duration = Date.now() - start;

      if (exitCode !== 0) {
        throw new BridgeError(
          `Gemini CLI exited with code ${exitCode}: ${stderr}`,
          exitCode,
          this.name,
        );
      }

      try {
        const data = JSON.parse(stdout) as {
          result?: string;
          response?: string;
        };

        return {
          content: data.result ?? data.response ?? stdout.trim(),
          tokens: { input: 0, output: 0 },
          model: this.model,
          duration,
        };
      } catch {
        return {
          content: stdout.trim(),
          tokens: { input: 0, output: 0 },
          model: this.model,
          duration,
        };
      }
    } catch (err) {
      if (err instanceof BridgeError) {
        throw err;
      }
      throw new BridgeError(
        `Failed to spawn gemini: ${err instanceof Error ? err.message : String(err)}`,
        -1,
        this.name,
      );
    }
  }

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    const args = [
      "-p",
      prompt,
      "--output-format",
      "stream-json",
      "--model",
      this.model,
    ];

    const proc = spawn(this.binaryPath, args, {
      env: this.buildSpawnEnv(),
      stdio: ["ignore", "pipe", "pipe"],
      signal: options?.signal,
    });

    let buffer = "";

    for await (const chunk of proc.stdout) {
      buffer += chunk.toString();
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";

      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const parsed = JSON.parse(line) as {
            type: string;
            content?: string;
            text?: string;
          };

          if (parsed.content || parsed.text) {
            yield {
              type: "text",
              content: parsed.content ?? parsed.text ?? "",
            };
          }
        } catch {
          if (line.trim()) {
            yield { type: "text", content: line };
          }
        }
      }
    }

    yield { type: "done", content: "" };
  }
}
