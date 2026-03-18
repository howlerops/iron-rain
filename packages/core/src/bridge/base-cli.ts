import { spawn } from "node:child_process";
import type { CliPermissionMode } from "../config/schema.js";
import type {
  BridgeChunk,
  BridgeOptions,
  BridgeResult,
  CLIBridge,
} from "./types.js";

export abstract class BaseCLIBridge implements CLIBridge {
  readonly name: string;
  protected model: string;
  protected binaryPath: string;
  protected permissionMode: CliPermissionMode;

  constructor(opts: {
    name: string;
    model: string;
    binaryPath: string;
    permissionMode?: CliPermissionMode;
  }) {
    this.name = opts.name;
    this.model = opts.model;
    this.binaryPath = opts.binaryPath;
    this.permissionMode = opts.permissionMode ?? "supervised";
  }

  protected buildSpawnEnv(): NodeJS.ProcessEnv {
    return process.env;
  }

  async available(): Promise<boolean> {
    return new Promise((resolve) => {
      const proc = spawn(this.binaryPath, ["--version"], {
        env: this.buildSpawnEnv(),
        stdio: ["ignore", "pipe", "pipe"],
        timeout: 5000,
      });
      proc.on("close", (code) => resolve(code === 0));
      proc.on("error", () => resolve(false));
    });
  }

  protected spawnAndCollect(
    args: string[],
    signal?: AbortSignal,
  ): Promise<{ stdout: string; stderr: string; exitCode: number }> {
    return new Promise((resolve, reject) => {
      const proc = spawn(this.binaryPath, args, {
        env: this.buildSpawnEnv(),
        stdio: ["ignore", "pipe", "pipe"],
        signal,
      });

      let stdout = "";
      let stderr = "";

      proc.stdout.on("data", (chunk: Buffer) => {
        stdout += chunk.toString();
      });
      proc.stderr.on("data", (chunk: Buffer) => {
        stderr += chunk.toString();
      });

      proc.on("close", (code) => {
        resolve({ stdout, stderr, exitCode: code ?? -1 });
      });

      proc.on("error", (err) => {
        reject(err);
      });
    });
  }

  abstract execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult>;
  abstract stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk>;
}
