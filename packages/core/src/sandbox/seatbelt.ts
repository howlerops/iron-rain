import { execSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import type { SandboxConfig, SandboxExecutor } from "./types.js";

/**
 * macOS Seatbelt (sandbox-exec) backend.
 * Restricts filesystem writes and network access.
 */
export class SeatbeltExecutor implements SandboxExecutor {
  readonly backend = "seatbelt" as const;

  async isAvailable(): Promise<boolean> {
    if (process.platform !== "darwin") return false;
    try {
      execSync("which sandbox-exec", { stdio: "pipe" });
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
    const profilePath = this.generateProfile(config);
    return {
      command: "sandbox-exec",
      args: ["-f", profilePath, command, ...args],
    };
  }

  private generateProfile(config: SandboxConfig): string {
    const cwd = process.cwd();
    const writePaths = [
      cwd,
      join(homedir(), ".iron-rain"),
      "/tmp",
      ...(config.allowedWritePaths?.map((p) => resolve(p)) ?? []),
    ];

    const lines = [
      "(version 1)",
      "(deny default)",
      "(allow process*)",
      "(allow sysctl-read)",
      "(allow mach-lookup)",
      // Allow reading from anywhere
      "(allow file-read*)",
      // Allow writing only to specific paths
      ...writePaths.map(
        (p) => `(allow file-write* (subpath "${p}"))`,
      ),
      // Allow executing binaries
      "(allow process-exec)",
    ];

    // Network control
    if (config.allowNetwork) {
      lines.push("(allow network*)");
    } else {
      // Allow localhost only
      lines.push("(allow network* (local ip \"localhost:*\"))");
      lines.push("(allow network* (remote ip \"localhost:*\"))");
    }

    const profile = lines.join("\n");
    const profileDir = join(homedir(), ".iron-rain", "sandbox");
    mkdirSync(profileDir, { recursive: true });
    const profilePath = join(profileDir, "current.sb");
    writeFileSync(profilePath, profile, "utf-8");

    return profilePath;
  }
}
