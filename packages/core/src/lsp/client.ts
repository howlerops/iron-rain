/**
 * LSP Client — connects to workspace language servers for diagnostics.
 */

import { type ChildProcess, spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve } from "node:path";

export interface LSPServerConfig {
  command: string;
  args: string[];
}

export interface Diagnostic {
  file: string;
  line: number;
  column: number;
  severity: "error" | "warning" | "info" | "hint";
  message: string;
  source?: string;
}

export interface LSPClientConfig {
  enabled: boolean;
  servers?: Record<string, LSPServerConfig>;
}

export const DEFAULT_LSP_CONFIG: LSPClientConfig = {
  enabled: false,
};

/**
 * Auto-detect which language servers might be available.
 */
export function detectLanguageServers(cwd: string): Record<string, LSPServerConfig> {
  const servers: Record<string, LSPServerConfig> = {};

  if (existsSync(resolve(cwd, "tsconfig.json"))) {
    servers.typescript = {
      command: "typescript-language-server",
      args: ["--stdio"],
    };
  }

  if (existsSync(resolve(cwd, "pyproject.toml")) || existsSync(resolve(cwd, "setup.py"))) {
    servers.python = {
      command: "pylsp",
      args: [],
    };
  }

  if (existsSync(resolve(cwd, "go.mod"))) {
    servers.go = {
      command: "gopls",
      args: ["serve"],
    };
  }

  if (existsSync(resolve(cwd, "Cargo.toml"))) {
    servers.rust = {
      command: "rust-analyzer",
      args: [],
    };
  }

  return servers;
}

/**
 * Simple LSP client that can request diagnostics.
 * Uses JSON-RPC 2.0 over stdio.
 */
export class LSPClient {
  private process: ChildProcess | null = null;
  private serverName: string;
  private config: LSPServerConfig;
  private nextId = 1;
  private buffer = "";
  private pendingRequests = new Map<
    number,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  >();

  constructor(name: string, config: LSPServerConfig) {
    this.serverName = name;
    this.config = config;
  }

  get name(): string {
    return this.serverName;
  }

  async connect(rootUri: string): Promise<void> {
    this.process = spawn(this.config.command, this.config.args, {
      stdio: ["pipe", "pipe", "pipe"],
    });

    this.process.stdout!.on("data", (data: Buffer) => {
      this.handleData(data.toString());
    });

    this.process.on("close", () => {
      for (const [, pending] of this.pendingRequests) {
        pending.reject(new Error(`LSP server ${this.serverName} disconnected`));
      }
      this.pendingRequests.clear();
    });

    // Initialize
    await this.sendRequest("initialize", {
      processId: process.pid,
      rootUri: `file://${rootUri}`,
      capabilities: {},
    });

    this.sendNotification("initialized", {});
  }

  async disconnect(): Promise<void> {
    try {
      await this.sendRequest("shutdown", {});
      this.sendNotification("exit", {});
    } catch {
      // Ignore shutdown errors
    }
    this.process?.kill();
    this.process = null;
  }

  private sendNotification(method: string, params: Record<string, unknown>): void {
    const msg = JSON.stringify({ jsonrpc: "2.0", method, params });
    const header = `Content-Length: ${Buffer.byteLength(msg)}\r\n\r\n`;
    this.process?.stdin?.write(header + msg);
  }

  private sendRequest(method: string, params: Record<string, unknown>): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const id = this.nextId++;
      const msg = JSON.stringify({ jsonrpc: "2.0", id, method, params });
      const header = `Content-Length: ${Buffer.byteLength(msg)}\r\n\r\n`;

      const timeout = setTimeout(() => {
        this.pendingRequests.delete(id);
        reject(new Error(`LSP request ${method} timed out`));
      }, 10000);

      this.pendingRequests.set(id, {
        resolve: (v) => {
          clearTimeout(timeout);
          resolve(v);
        },
        reject: (e) => {
          clearTimeout(timeout);
          reject(e);
        },
      });

      this.process?.stdin?.write(header + msg);
    });
  }

  private handleData(data: string): void {
    this.buffer += data;

    // Parse LSP Content-Length headers + JSON body
    while (this.buffer.length > 0) {
      const headerEnd = this.buffer.indexOf("\r\n\r\n");
      if (headerEnd < 0) break;

      const header = this.buffer.slice(0, headerEnd);
      const lengthMatch = header.match(/Content-Length:\s*(\d+)/i);
      if (!lengthMatch) {
        this.buffer = this.buffer.slice(headerEnd + 4);
        continue;
      }

      const contentLength = parseInt(lengthMatch[1], 10);
      const bodyStart = headerEnd + 4;
      if (this.buffer.length < bodyStart + contentLength) break;

      const body = this.buffer.slice(bodyStart, bodyStart + contentLength);
      this.buffer = this.buffer.slice(bodyStart + contentLength);

      try {
        const msg = JSON.parse(body) as {
          id?: number;
          result?: unknown;
          error?: { message: string };
        };
        if (msg.id !== undefined && this.pendingRequests.has(msg.id)) {
          const pending = this.pendingRequests.get(msg.id)!;
          this.pendingRequests.delete(msg.id);

          if (msg.error) {
            pending.reject(new Error(msg.error.message));
          } else {
            pending.resolve(msg.result);
          }
        }
      } catch {
        // Skip malformed messages
      }
    }
  }
}
