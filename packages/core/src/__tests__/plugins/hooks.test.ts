import { describe, expect, it } from "bun:test";
import { HookEmitter } from "../../plugins/hooks.js";

describe("HookEmitter", () => {
  it("registers and emits events", async () => {
    const emitter = new HookEmitter();
    let received = false;

    emitter.on("onSessionStart", () => {
      received = true;
    });

    await emitter.emit("onSessionStart", { test: true });
    expect(received).toBe(true);
  });

  it("passes event data to handlers", async () => {
    const emitter = new HookEmitter();
    let eventData: unknown = null;

    emitter.on("onToolCall", (data) => {
      eventData = data;
    });

    await emitter.emit("onToolCall", { tool: "read_file" });
    expect(eventData).toBeTruthy();
    expect((eventData as { event: string }).event).toBe("onToolCall");
    expect((eventData as { payload: { tool: string } }).payload.tool).toBe(
      "read_file",
    );
  });

  it("supports multiple handlers for same event", async () => {
    const emitter = new HookEmitter();
    const calls: number[] = [];

    emitter.on("onCommit", () => {
      calls.push(1);
    });
    emitter.on("onCommit", () => {
      calls.push(2);
    });

    await emitter.emit("onCommit");
    expect(calls).toEqual([1, 2]);
  });

  it("unsubscribe function removes handler", async () => {
    const emitter = new HookEmitter();
    let count = 0;

    const unsub = emitter.on("onError", () => {
      count++;
    });
    await emitter.emit("onError");
    expect(count).toBe(1);

    unsub();
    await emitter.emit("onError");
    expect(count).toBe(1);
  });

  it("off removes all handlers for event", async () => {
    const emitter = new HookEmitter();
    let count = 0;

    emitter.on("onSessionEnd", () => {
      count++;
    });
    emitter.on("onSessionEnd", () => {
      count++;
    });
    emitter.off("onSessionEnd");

    await emitter.emit("onSessionEnd");
    expect(count).toBe(0);
  });

  it("clear removes all handlers", () => {
    const emitter = new HookEmitter();
    emitter.on("onToolCall", () => {});
    emitter.on("onSessionStart", () => {});
    emitter.clear();

    expect(emitter.listenerCount("onToolCall")).toBe(0);
    expect(emitter.listenerCount("onSessionStart")).toBe(0);
  });

  it("listenerCount returns correct count", () => {
    const emitter = new HookEmitter();
    expect(emitter.listenerCount("onCommit")).toBe(0);

    emitter.on("onCommit", () => {});
    expect(emitter.listenerCount("onCommit")).toBe(1);

    emitter.on("onCommit", () => {});
    expect(emitter.listenerCount("onCommit")).toBe(2);
  });

  it("handler errors do not break emission", async () => {
    const emitter = new HookEmitter();
    let secondCalled = false;

    emitter.on("onToolResult", () => {
      throw new Error("handler error");
    });
    emitter.on("onToolResult", () => {
      secondCalled = true;
    });

    await emitter.emit("onToolResult");
    expect(secondCalled).toBe(true);
  });

  it("emitting unregistered event is a no-op", async () => {
    const emitter = new HookEmitter();
    await emitter.emit("onCheckpoint"); // Should not throw
  });
});
