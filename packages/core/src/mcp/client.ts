/**
 * MCP Client — connects to an MCP server over stdio transport (JSON-RPC 2.0).
 */
import { type ChildProcess, spawn } from "node:child_process";
import type {
  JsonRpcRequest,
  JsonRpcResponse,
  MCPCallToolResult,
  MCPInitializeResult,
  MCPServerConfig,
  MCPTool,
  MCPToolListResult,
  MCPToolResult,
} from "./types.js";

const PROTOCOL_VERSION = "2024-11-05";
const CONNECT_TIMEOUT_MS = 10000;

export class MCPClient {
  private config: MCPServerConfig;
  private serverName: string;
  private process: ChildProcess | null = null;
  private nextId = 1;
  private pendingRequests = new Map<
    number,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  >();
  private buffer = "";
  private connected = false;
  private tools: MCPTool[] = [];

  constructor(serverName: string, config: MCPServerConfig) {
    this.serverName = serverName;
    this.config = config;
  }

  get name(): string {
    return this.serverName;
  }

  get isConnected(): boolean {
    return this.connected;
  }

  async connect(): Promise<MCPInitializeResult> {
    const env = { ...process.env, ...(this.config.env ?? {}) };

    this.process = spawn(this.config.command, this.config.args ?? [], {
      stdio: ["pipe", "pipe", "pipe"],
      env,
    });

    this.process.stdout!.on("data", (data: Buffer) => {
      this.handleData(data.toString());
    });

    this.process.stderr!.on("data", (data: Buffer) => {
      // Log MCP server errors to stderr for debugging
      process.stderr.write(`[mcp:${this.serverName}] ${data.toString()}`);
    });

    this.process.on("close", () => {
      this.connected = false;
      // Reject all pending requests
      for (const [, pending] of this.pendingRequests) {
        pending.reject(new Error(`MCP server ${this.serverName} disconnected`));
      }
      this.pendingRequests.clear();
    });

    // Send initialize request
    const result = (await this.sendRequest("initialize", {
      protocolVersion: PROTOCOL_VERSION,
      capabilities: {},
      clientInfo: { name: "iron-rain", version: "0.1.6" },
    })) as MCPInitializeResult;

    // Send initialized notification
    this.sendNotification("notifications/initialized", {});

    this.connected = true;
    return result;
  }

  async listTools(): Promise<MCPTool[]> {
    const result = (await this.sendRequest(
      "tools/list",
      {},
    )) as MCPToolListResult;
    this.tools = result.tools.map((t) => ({
      name: t.name,
      description: t.description ?? "",
      inputSchema: t.inputSchema ?? {},
      server: this.serverName,
    }));
    return this.tools;
  }

  getCachedTools(): MCPTool[] {
    return this.tools;
  }

  async callTool(
    name: string,
    args: Record<string, unknown> = {},
  ): Promise<MCPToolResult> {
    const result = (await this.sendRequest("tools/call", {
      name,
      arguments: args,
    })) as MCPCallToolResult;

    return {
      content: result.content,
      isError: result.isError,
    };
  }

  async disconnect(): Promise<void> {
    this.connected = false;
    if (this.process) {
      this.process.kill();
      this.process = null;
    }
    this.pendingRequests.clear();
  }

  private sendNotification(
    method: string,
    params: Record<string, unknown>,
  ): void {
    const msg = JSON.stringify({ jsonrpc: "2.0", method, params });
    this.process?.stdin?.write(msg + "\n");
  }

  private sendRequest(
    method: string,
    params: Record<string, unknown>,
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const id = this.nextId++;
      const request: JsonRpcRequest = { jsonrpc: "2.0", id, method, params };

      this.pendingRequests.set(id, { resolve, reject });

      const timeout = setTimeout(() => {
        this.pendingRequests.delete(id);
        reject(
          new Error(
            `MCP request ${method} timed out after ${CONNECT_TIMEOUT_MS}ms`,
          ),
        );
      }, CONNECT_TIMEOUT_MS);

      // Wrap resolve/reject to clear timeout
      const origResolve = resolve;
      const origReject = reject;
      this.pendingRequests.set(id, {
        resolve: (v) => {
          clearTimeout(timeout);
          origResolve(v);
        },
        reject: (e) => {
          clearTimeout(timeout);
          origReject(e);
        },
      });

      this.process?.stdin?.write(JSON.stringify(request) + "\n");
    });
  }

  private handleData(data: string): void {
    this.buffer += data;

    // Process complete JSON-RPC messages (newline-delimited)
    const lines = this.buffer.split("\n");
    this.buffer = lines.pop() ?? "";

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) continue;

      try {
        const msg = JSON.parse(trimmed) as JsonRpcResponse;
        if (msg.id !== undefined && this.pendingRequests.has(msg.id)) {
          const pending = this.pendingRequests.get(msg.id)!;
          this.pendingRequests.delete(msg.id);

          if (msg.error) {
            pending.reject(new Error(`MCP error: ${msg.error.message}`));
          } else {
            pending.resolve(msg.result);
          }
        }
      } catch {
        // Skip malformed lines
      }
    }
  }
}
