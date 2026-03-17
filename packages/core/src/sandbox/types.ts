export type SandboxBackend = "none" | "seatbelt" | "docker" | "gvisor";

export interface SandboxResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface SandboxConfig {
  backend: SandboxBackend;
  allowNetwork: boolean;
  allowedWritePaths?: string[];
}

export interface SandboxExecutor {
  readonly backend: SandboxBackend;

  /**
   * Check if this sandbox backend is available on the current platform.
   */
  isAvailable(): Promise<boolean>;

  /**
   * Wrap a command for sandboxed execution.
   * Returns the modified command and args array.
   */
  wrapCommand(
    command: string,
    args: string[],
    config: SandboxConfig,
  ): { command: string; args: string[] };
}

export const DEFAULT_SANDBOX_CONFIG: SandboxConfig = {
  backend: "none",
  allowNetwork: false,
};
