/**
 * Hook/lifecycle event system for Iron Rain plugins.
 */

export type HookEvent =
  | "onToolCall"
  | "onToolResult"
  | "onSessionStart"
  | "onSessionEnd"
  | "onCommit"
  | "onError"
  | "onCheckpoint"
  | "beforeDispatch"
  | "afterDispatch";

export type HookHandler = (data: HookEventData) => void | Promise<void>;

export interface HookEventData {
  event: HookEvent;
  timestamp: number;
  payload: Record<string, unknown>;
}

/**
 * Emits lifecycle hooks to registered handlers.
 */
export class HookEmitter {
  private handlers = new Map<HookEvent, HookHandler[]>();

  /**
   * Register a handler for a specific event.
   */
  on(event: HookEvent, handler: HookHandler): () => void {
    const list = this.handlers.get(event) ?? [];
    list.push(handler);
    this.handlers.set(event, list);

    // Return unsubscribe function
    return () => {
      const current = this.handlers.get(event);
      if (current) {
        const idx = current.indexOf(handler);
        if (idx >= 0) current.splice(idx, 1);
      }
    };
  }

  /**
   * Emit an event to all registered handlers.
   */
  async emit(
    event: HookEvent,
    payload: Record<string, unknown> = {},
  ): Promise<void> {
    const handlers = this.handlers.get(event);
    if (!handlers || handlers.length === 0) return;

    const data: HookEventData = {
      event,
      timestamp: Date.now(),
      payload,
    };

    for (const handler of handlers) {
      try {
        await handler(data);
      } catch (err) {
        // Hook errors should not break the main flow
        process.stderr.write(
          `[hooks] Error in ${event} handler: ${err instanceof Error ? err.message : String(err)}\n`,
        );
      }
    }
  }

  /**
   * Remove all handlers for a specific event.
   */
  off(event: HookEvent): void {
    this.handlers.delete(event);
  }

  /**
   * Remove all handlers.
   */
  clear(): void {
    this.handlers.clear();
  }

  /**
   * Get count of registered handlers for an event.
   */
  listenerCount(event: HookEvent): number {
    return this.handlers.get(event)?.length ?? 0;
  }
}
