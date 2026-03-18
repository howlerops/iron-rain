import { spawn } from "node:child_process";
import { appendFileSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { CliPermissionMode } from "../config/schema.js";
import { BaseCLIBridge } from "./base-cli.js";
import { BridgeError } from "./errors.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";

const DEBUG_LOG = join(homedir(), ".iron-rain", "debug.log");

function debugLog(msg: string) {
  try {
    mkdirSync(join(homedir(), ".iron-rain"), { recursive: true });
    appendFileSync(DEBUG_LOG, `[${new Date().toISOString()}] ${msg}\n`);
  } catch {}
}

/** Build a concise display label from tool name + input args. */
function toolDisplayName(name: string, input?: Record<string, any>): string {
  if (!input) return name;
  const filePath = input.file_path ?? input.path;
  if (filePath && typeof filePath === "string") {
    const base = filePath.split("/").pop() ?? filePath;
    return `${name} ${base}`;
  }
  if (input.pattern && typeof input.pattern === "string") {
    const p =
      input.pattern.length > 30
        ? `${input.pattern.slice(0, 27)}...`
        : input.pattern;
    return `${name} ${p}`;
  }
  if (input.command && typeof input.command === "string") {
    const c =
      input.command.length > 40
        ? `${input.command.slice(0, 37)}...`
        : input.command;
    return `${name}: ${c}`;
  }
  return name;
}

export class ClaudeCodeBridge extends BaseCLIBridge {
  private sessionId: string | null = null;

  constructor(opts: {
    model?: string;
    binaryPath?: string;
    permissionMode?: CliPermissionMode;
  }) {
    super({
      name: "claude-code",
      model: opts.model ?? "sonnet",
      binaryPath: opts.binaryPath ?? "claude",
      permissionMode: opts.permissionMode,
    });
  }

  protected override buildSpawnEnv(): NodeJS.ProcessEnv {
    return { ...process.env, CLAUDECODE: "" };
  }

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const useResume = this.sessionId && options?.conversationHistory?.length;
    const args = [
      "-p",
      prompt,
      "--output-format",
      "json",
      "--model",
      this.model,
      ...(useResume
        ? ["--resume", this.sessionId!]
        : ["--no-session-persistence"]),
    ];

    if (this.permissionMode === "auto") {
      args.push("--dangerously-skip-permissions");
    }

    if (options?.systemPrompt) {
      args.push("--append-system-prompt", options.systemPrompt);
    }

    try {
      const { stdout, stderr, exitCode } = await this.spawnAndCollect(
        args,
        options?.signal,
      );
      const duration = Date.now() - start;

      if (exitCode !== 0) {
        throw new BridgeError(
          `Claude Code exited with code ${exitCode}: ${stderr}`,
          exitCode,
          this.name,
        );
      }

      try {
        const data = JSON.parse(stdout) as {
          result: string;
          cost_usd?: number;
          duration_ms?: number;
          num_turns?: number;
          session_id?: string;
        };

        if (data.session_id) this.sessionId = data.session_id;
        return {
          content: data.result ?? stdout,
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
        `Failed to spawn claude: ${err instanceof Error ? err.message : String(err)}`,
        -1,
        this.name,
      );
    }
  }

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    const useResume = this.sessionId && options?.conversationHistory?.length;
    const args = [
      "-p",
      prompt,
      "--output-format",
      "stream-json",
      "--verbose",
      "--model",
      this.model,
      ...(useResume
        ? ["--resume", this.sessionId!]
        : ["--no-session-persistence"]),
    ];

    if (this.permissionMode === "auto") {
      args.push("--dangerously-skip-permissions");
    }

    if (options?.systemPrompt) {
      args.push("--append-system-prompt", options.systemPrompt);
    }

    debugLog(`stream: spawning ${this.binaryPath} ${args.join(" ")}`);

    const proc = spawn(this.binaryPath, args, {
      env: this.buildSpawnEnv(),
      stdio: ["ignore", "pipe", "pipe"],
      signal: options?.signal,
    });

    let stderrBuf = "";
    proc.stderr.on("data", (chunk: Buffer) => {
      stderrBuf += chunk.toString();
    });
    proc.on("close", (code) => {
      debugLog(
        `stream: process exited code=${code} stderr=${stderrBuf.slice(0, 500)}`,
      );
    });

    const reader = proc.stdout;
    let buffer = "";
    let chunkCount = 0;
    let yieldCount = 0;
    const activeTools = new Map<string, string>(); // id -> label

    try {
      for await (const chunk of reader) {
        buffer += chunk.toString();
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";

        for (const line of lines) {
          if (!line.trim()) continue;
          chunkCount++;
          try {
            const parsed = JSON.parse(line) as Record<string, any>;
            debugLog(`stream: line ${chunkCount} type=${parsed.type}`);

            if (parsed.type === "assistant" && parsed.message?.content) {
              const content = parsed.message.content;
              if (Array.isArray(content)) {
                for (const block of content) {
                  if (block.type === "text" && block.text) {
                    debugLog(
                      `stream: yielding text (${block.text.length} chars)`,
                    );
                    yieldCount++;
                    yield { type: "text", content: block.text };
                  } else if (
                    block.type === "tool_use" &&
                    block.name &&
                    block.id
                  ) {
                    const label = toolDisplayName(block.name, block.input);
                    activeTools.set(block.id, label);
                    yield {
                      type: "tool_use" as const,
                      content: label,
                      toolCall: {
                        id: block.id,
                        name: label,
                        status: "start" as const,
                      },
                    };
                  }
                }
              }
            } else if (
              parsed.type === "content_block_delta" &&
              parsed.delta?.text
            ) {
              yieldCount++;
              yield { type: "text", content: parsed.delta.text };
            } else if (
              parsed.type === "tool_result" ||
              (parsed.type === "assistant" &&
                parsed.message?.content &&
                Array.isArray(parsed.message.content) &&
                parsed.message.content.some(
                  (b: any) => b.type === "tool_result",
                ))
            ) {
              // Handle tool_result events — mark corresponding tool as ended
              const toolUseId =
                parsed.tool_use_id ?? parsed.content?.tool_use_id;
              if (toolUseId && activeTools.has(toolUseId)) {
                const name = activeTools.get(toolUseId)!;
                activeTools.delete(toolUseId);
                yield {
                  type: "tool_use" as const,
                  content: name,
                  toolCall: { id: toolUseId, name, status: "end" as const },
                };
              }
            } else if (parsed.type === "result") {
              // Close any remaining active tools before done
              for (const [id, name] of activeTools) {
                yield {
                  type: "tool_use" as const,
                  content: name,
                  toolCall: { id, name, status: "end" as const },
                };
              }
              activeTools.clear();
              if (parsed.session_id) this.sessionId = parsed.session_id;
              debugLog(
                `stream: result event, subtype=${parsed.subtype}, yieldCount=${yieldCount}`,
              );
              if (
                yieldCount === 0 &&
                parsed.result &&
                typeof parsed.result === "string"
              ) {
                yield { type: "text", content: parsed.result };
              }
              yield { type: "done", content: "" };
              return;
            }
          } catch (e: any) {
            debugLog(`stream: parse error: ${e.message}`);
          }
        }
      }
    } catch (err) {
      // AbortError when signal fires — not a real error
      if (options?.signal?.aborted) {
        yield { type: "done", content: "" };
        return;
      }
      yield {
        type: "error",
        content: err instanceof Error ? err.message : String(err),
      };
    }

    debugLog(
      `stream: stdout ended, chunkCount=${chunkCount} yieldCount=${yieldCount}`,
    );
    yield { type: "done", content: "" };
  }
}
