import { describe, expect, it } from "bun:test";
import type { HookEvent } from "../../plugins/hooks.js";
import { HookEmitter } from "../../plugins/hooks.js";

describe("Hook lifecycle events", () => {
  it("beforeDispatch fires with prompt and slot", async () => {
    const emitter = new HookEmitter();
    let payload: Record<string, unknown> | null = null;

    emitter.on("beforeDispatch", (data) => {
      payload = data.payload;
    });

    await emitter.emit("beforeDispatch", {
      prompt: "test prompt",
      slot: "main",
    });

    expect(payload).toBeTruthy();
    expect(payload!.prompt).toBe("test prompt");
    expect(payload!.slot).toBe("main");
  });

  it("afterDispatch fires with duration and tokens", async () => {
    const emitter = new HookEmitter();
    let payload: Record<string, unknown> | null = null;

    emitter.on("afterDispatch", (data) => {
      payload = data.payload;
    });

    await emitter.emit("afterDispatch", {
      prompt: "test",
      slot: "main",
      duration: 1234,
      tokens: 500,
    });

    expect(payload).toBeTruthy();
    expect(payload!.duration).toBe(1234);
    expect(payload!.tokens).toBe(500);
  });

  it("onToolCall and onToolResult fire with tool name", async () => {
    const emitter = new HookEmitter();
    const events: Array<{ event: HookEvent; tool: unknown }> = [];

    emitter.on("onToolCall", (data) => {
      events.push({ event: data.event, tool: data.payload.tool });
    });
    emitter.on("onToolResult", (data) => {
      events.push({ event: data.event, tool: data.payload.tool });
    });

    await emitter.emit("onToolCall", { tool: "read_file" });
    await emitter.emit("onToolResult", { tool: "read_file" });

    expect(events).toHaveLength(2);
    expect(events[0].event).toBe("onToolCall");
    expect(events[0].tool).toBe("read_file");
    expect(events[1].event).toBe("onToolResult");
    expect(events[1].tool).toBe("read_file");
  });

  it("onError fires with error message", async () => {
    const emitter = new HookEmitter();
    let payload: Record<string, unknown> | null = null;

    emitter.on("onError", (data) => {
      payload = data.payload;
    });

    await emitter.emit("onError", {
      error: "Something went wrong",
      prompt: "test",
    });

    expect(payload).toBeTruthy();
    expect(payload!.error).toBe("Something went wrong");
  });

  it("full lifecycle fires events in order", async () => {
    const emitter = new HookEmitter();
    const eventOrder: HookEvent[] = [];

    for (const event of [
      "beforeDispatch",
      "onToolCall",
      "onToolResult",
      "afterDispatch",
    ] as HookEvent[]) {
      emitter.on(event, (data) => {
        eventOrder.push(data.event);
      });
    }

    await emitter.emit("beforeDispatch", { prompt: "test", slot: "main" });
    await emitter.emit("onToolCall", { tool: "write_file" });
    await emitter.emit("onToolResult", { tool: "write_file" });
    await emitter.emit("afterDispatch", {
      prompt: "test",
      slot: "main",
      duration: 100,
      tokens: 50,
    });

    expect(eventOrder).toEqual([
      "beforeDispatch",
      "onToolCall",
      "onToolResult",
      "afterDispatch",
    ]);
  });

  it("error in beforeDispatch handler does not prevent afterDispatch", async () => {
    const emitter = new HookEmitter();
    let afterFired = false;

    emitter.on("beforeDispatch", () => {
      throw new Error("plugin error");
    });
    emitter.on("afterDispatch", () => {
      afterFired = true;
    });

    await emitter.emit("beforeDispatch", { prompt: "test", slot: "main" });
    // After the error, afterDispatch should still be fireable
    await emitter.emit("afterDispatch", {
      prompt: "test",
      slot: "main",
      duration: 0,
      tokens: 0,
    });

    expect(afterFired).toBe(true);
  });
});
