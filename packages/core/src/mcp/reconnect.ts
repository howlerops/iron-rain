/**
 * MCP reconnection logic with exponential backoff.
 */

export interface ReconnectConfig {
  maxAttempts: number;
  baseDelayMs: number;
  maxDelayMs: number;
}

export const DEFAULT_RECONNECT_CONFIG: ReconnectConfig = {
  maxAttempts: 5,
  baseDelayMs: 1000,
  maxDelayMs: 30000,
};

/**
 * Calculate backoff delay with jitter for reconnection attempts.
 */
export function reconnectDelay(attempt: number, config: ReconnectConfig): number {
  const exponential = config.baseDelayMs * 2 ** attempt;
  const capped = Math.min(exponential, config.maxDelayMs);
  const jitter = capped * (0.5 + Math.random() * 0.5);
  return Math.round(jitter);
}

/**
 * Detect duplicate tool names across MCP servers and namespace them.
 */
export function namespaceDuplicateTools<T extends { name: string; server: string }>(
  tools: T[],
): T[] {
  const nameCount = new Map<string, number>();
  for (const tool of tools) {
    nameCount.set(tool.name, (nameCount.get(tool.name) ?? 0) + 1);
  }

  return tools.map((tool) => {
    if ((nameCount.get(tool.name) ?? 0) > 1) {
      return { ...tool, name: `${tool.server}.${tool.name}` };
    }
    return tool;
  });
}

/**
 * Resolve a tool name that may be plain or namespaced (server.toolName).
 */
export function resolveToolName(
  name: string,
  tools: Array<{ name: string; server: string }>,
): { server: string; toolName: string } | null {
  // Try exact match first (handles namespaced names)
  const exact = tools.find((t) => t.name === name);
  if (exact) return { server: exact.server, toolName: exact.name };

  // Try namespaced format: server.toolName
  const dotIndex = name.indexOf(".");
  if (dotIndex > 0) {
    const server = name.slice(0, dotIndex);
    const toolName = name.slice(dotIndex + 1);
    const match = tools.find((t) => t.server === server && t.name === toolName);
    if (match) return { server: match.server, toolName: match.name };
  }

  // Try first match by plain name
  const plain = tools.find((t) => t.name === name || t.name.endsWith(`.${name}`));
  if (plain) return { server: plain.server, toolName: plain.name };

  return null;
}
