import type { CliPermissionMode } from "../config/schema.js";
import { BaseCLIBridge } from "./base-cli.js";
import { BridgeError } from "./errors.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";

export class CodexBridge extends BaseCLIBridge {
  constructor(opts: {
    model?: string;
    binaryPath?: string;
    permissionMode?: CliPermissionMode;
  }) {
    super({
      name: "codex",
      model: opts.model ?? "gpt-5.4",
      binaryPath: opts.binaryPath ?? "codex",
      permissionMode: opts.permissionMode,
    });
  }

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const args = [
      "exec",
      prompt,
      "-c",
      `model="${this.model}"`,
      ...(this.permissionMode === "auto"
        ? ["--approval-mode", "full-auto"]
        : []),
    ];

    try {
      const { stdout, stderr, exitCode } = await this.spawnAndCollect(
        args,
        options?.signal,
      );

      const duration = Date.now() - start;

      if (exitCode !== 0) {
        throw new BridgeError(
          `Codex exited with code ${exitCode}: ${stderr}`,
          exitCode,
          this.name,
        );
      }

      return {
        content: stdout.trim(),
        tokens: { input: 0, output: 0 },
        model: this.model,
        duration,
      };
    } catch (err) {
      if (err instanceof BridgeError) {
        throw err;
      }
      throw new BridgeError(
        `Failed to spawn codex: ${err instanceof Error ? err.message : String(err)}`,
        -1,
        this.name,
      );
    }
  }

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    // Codex exec doesn't support streaming — execute and yield all at once
    try {
      const result = await this.execute(prompt, options);
      yield { type: "text", content: result.content };
    } catch (err) {
      yield {
        type: "error",
        content: err instanceof Error ? err.message : String(err),
      };
    }
    yield { type: "done", content: "" };
  }
}
