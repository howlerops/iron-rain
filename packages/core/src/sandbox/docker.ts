/**
 * Docker sandbox executor — runs commands in an isolated container.
 */

import { execSync } from "node:child_process";
import { resolve } from "node:path";
import type { SandboxConfig, SandboxExecutor } from "./types.js";

export interface DockerConfig {
  image: string;
  memoryLimit: string;
  cpuLimit: string;
}

export const DEFAULT_DOCKER_CONFIG: DockerConfig = {
  image: "node:20-slim",
  memoryLimit: "2g",
  cpuLimit: "2",
};

/**
 * Docker sandbox executor.
 * Wraps commands to run inside a Docker container.
 */
export class DockerExecutor implements SandboxExecutor {
  readonly backend = "docker" as const;
  private dockerConfig: DockerConfig;

  constructor(dockerConfig: DockerConfig = DEFAULT_DOCKER_CONFIG) {
    this.dockerConfig = dockerConfig;
  }

  async isAvailable(): Promise<boolean> {
    try {
      execSync("docker info", { stdio: "pipe", timeout: 5000 });
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
    const dockerArgs = [
      "run",
      "--rm",
      `-v${cwd}:/workspace`,
      "-w/workspace",
      `--memory=${this.dockerConfig.memoryLimit}`,
      `--cpus=${this.dockerConfig.cpuLimit}`,
    ];

    if (!config.allowNetwork) {
      dockerArgs.push("--network=none");
    }

    if (config.allowedWritePaths) {
      for (const path of config.allowedWritePaths) {
        const abs = resolve(path);
        dockerArgs.push(`-v${abs}:${abs}`);
      }
    }

    dockerArgs.push(this.dockerConfig.image, command, ...args);

    return { command: "docker", args: dockerArgs };
  }
}
