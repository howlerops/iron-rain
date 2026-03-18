/**
 * MCP Manager — manages multiple MCP server connections.
 * Loads config from .mcp.json or iron-rain.json mcpServers field.
 */
import { MCPClient } from "./client.js";
import { namespaceDuplicateTools } from "./reconnect.js";
import type { MCPServerConfig, MCPTool, MCPToolResult } from "./types.js";

export interface MCPManagerConfig {
  mcpServers?: Record<string, MCPServerConfig>;
}

export class MCPManager {
  private clients = new Map<string, MCPClient>();
  private config: Record<string, MCPServerConfig>;

  constructor(config?: MCPManagerConfig) {
    this.config = config?.mcpServers ?? {};
  }

  /**
   * Initialize all configured MCP servers. Non-blocking — failures are logged but don't throw.
   */
  async connectAll(): Promise<void> {
    const entries = Object.entries(this.config);
    if (entries.length === 0) return;

    const _results = await Promise.allSettled(
      entries.map(async ([name, cfg]) => {
        const client = new MCPClient(name, cfg);
        try {
          await client.connect();
          await client.listTools();
          this.clients.set(name, client);
        } catch (err) {
          const msg = err instanceof Error ? err.message : String(err);
          if (process.env.DEBUG || process.env.IRON_RAIN_DEBUG) {
            process.stderr.write(
              `[mcp] Failed to connect to ${name}: ${msg}\n`,
            );
          }
        }
      }),
    );
  }

  /**
   * Connect a single server by name.
   */
  async connectServer(name: string): Promise<boolean> {
    const cfg = this.config[name];
    if (!cfg) return false;

    const client = new MCPClient(name, cfg);
    try {
      await client.connect();
      await client.listTools();
      this.clients.set(name, client);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Get all available tools across all connected servers.
   */
  getAllTools(): MCPTool[] {
    const tools: MCPTool[] = [];
    for (const client of this.clients.values()) {
      tools.push(...client.getCachedTools());
    }
    return namespaceDuplicateTools(tools);
  }

  /**
   * Call a tool by name, routing to the correct server.
   */
  async callTool(
    name: string,
    args: Record<string, unknown> = {},
  ): Promise<MCPToolResult> {
    for (const client of this.clients.values()) {
      const tool = client.getCachedTools().find((t) => t.name === name);
      if (tool) {
        return client.callTool(name, args);
      }
    }
    throw new Error(`MCP tool not found: ${name}`);
  }

  /**
   * Get formatted tool descriptions for system prompt injection.
   */
  getToolDescriptions(): string {
    const tools = this.getAllTools();
    if (tools.length === 0) return "";

    const byServer = new Map<string, MCPTool[]>();
    for (const tool of tools) {
      const list = byServer.get(tool.server) ?? [];
      list.push(tool);
      byServer.set(tool.server, list);
    }

    const parts: string[] = ["## Available MCP Tools"];
    for (const [server, serverTools] of byServer) {
      parts.push(`\n### ${server}`);
      for (const tool of serverTools) {
        parts.push(`- **${tool.name}**: ${tool.description}`);
      }
    }

    return parts.join("\n");
  }

  /**
   * Get status of all configured/connected servers.
   */
  getStatus(): Array<{ name: string; connected: boolean; toolCount: number }> {
    const status: Array<{
      name: string;
      connected: boolean;
      toolCount: number;
    }> = [];
    for (const name of Object.keys(this.config)) {
      const client = this.clients.get(name);
      status.push({
        name,
        connected: client?.isConnected ?? false,
        toolCount: client?.getCachedTools().length ?? 0,
      });
    }
    return status;
  }

  /**
   * Disconnect all servers.
   */
  async disconnectAll(): Promise<void> {
    for (const client of this.clients.values()) {
      await client.disconnect();
    }
    this.clients.clear();
  }

  /**
   * Number of connected servers.
   */
  get connectedCount(): number {
    return this.clients.size;
  }

  /**
   * Total tool count across all servers.
   */
  get totalToolCount(): number {
    return this.getAllTools().length;
  }
}
