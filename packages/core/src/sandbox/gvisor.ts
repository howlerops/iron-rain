/**
 * gVisor sandbox executor — Linux-only, uses runsc for OCI container sandboxing.
 */

import { execSync } from "node:child_process";
import type { SandboxConfig, SandboxExecutor } from "./types.js";

/**
 * gVisor (runsc) sandbox executor.
 */
export class GvisorExecutor implements SandboxExecutor {
  readonly backend = "gvisor" as const;

  async isAvailable(): Promise<boolean> {
    if (process.platform !== "linux") return false;
    try {
      execSync("which runsc", { stdio: "pipe", timeout: 3000 });
      return true;
    } catch {
      return false;
    }
  }

  wrapCommand(
    command: string,
    args: string[],
    config: SandboxConfig,
  ): { command: string; args: string[] } {
    const cwd = process.cwd();
    const runscArgs = ["do", `--root=${cwd}`];

    if (!config.allowNetwork) {
      runscArgs.push("--network=none");
    }

    runscArgs.push("--", command, ...args);

    return { command: "runsc", args: runscArgs };
  }
}
