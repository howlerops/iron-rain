import { DockerExecutor } from "./docker.js";
import { GvisorExecutor } from "./gvisor.js";
import { SeatbeltExecutor } from "./seatbelt.js";
import type {
  SandboxBackend,
  SandboxConfig,
  SandboxExecutor,
} from "./types.js";

export type { DockerConfig } from "./docker.js";
export { DEFAULT_DOCKER_CONFIG, DockerExecutor } from "./docker.js";
export { GvisorExecutor } from "./gvisor.js";
export { SeatbeltExecutor } from "./seatbelt.js";
export type {
  SandboxBackend,
  SandboxConfig,
  SandboxExecutor,
  SandboxResult,
} from "./types.js";
export { DEFAULT_SANDBOX_CONFIG } from "./types.js";

const executors = new Map<SandboxBackend, SandboxExecutor>([
  ["seatbelt", new SeatbeltExecutor()],
  ["docker", new DockerExecutor()],
  ["gvisor", new GvisorExecutor()],
]);

/**
 * Register a custom sandbox executor.
 */
export function registerSandboxBackend(
  name: SandboxBackend,
  executor: SandboxExecutor,
): void {
  executors.set(name, executor);
}

/**
 * Get the sandbox executor for a given backend.
 */
export function getSandboxExecutor(
  backend: SandboxBackend,
): SandboxExecutor | undefined {
  return executors.get(backend);
}

/**
 * Detect available sandbox backends on the current platform.
 */
export async function detectAvailableBackends(): Promise<SandboxBackend[]> {
  const available: SandboxBackend[] = ["none"];

  for (const [name, executor] of executors) {
    if (await executor.isAvailable()) {
      available.push(name);
    }
  }

  return available;
}

/**
 * Wrap a command through the configured sandbox, if any.
 */
export function wrapCommandForSandbox(
  command: string,
  args: string[],
  config: SandboxConfig,
): { command: string; args: string[] } {
  if (config.backend === "none") {
    return { command, args };
  }

  const executor = executors.get(config.backend);
  if (!executor) {
    return { command, args };
  }

  return executor.wrapCommand(command, args, config);
}
