import { describe, expect, test } from "bun:test";

import {
  BridgeError,
  CircuitBreaker,
  backoffDelay,
} from "../../bridge/errors.js";

describe("BridgeError", () => {
  test("isRetryable is true for 429", () => {
    expect(new BridgeError("rate limited", 429, "demo").isRetryable())
      .toBe(true);
  });

  test("isRetryable is true for 500-599", () => {
    expect(new BridgeError("server", 500, "demo").isRetryable()).toBe(true);
    expect(new BridgeError("server", 599, "demo").isRetryable()).toBe(true);
  });

  test("isRetryable is false for other 400-499", () => {
    expect(new BridgeError("bad request", 400, "demo").isRetryable())
      .toBe(false);
    expect(new BridgeError("not found", 404, "demo").isRetryable()).toBe(false);
  });
});

describe("CircuitBreaker", () => {
  test("opens at threshold and closes after reset window", async () => {
    const breaker = new CircuitBreaker(3, 5);

    breaker.recordFailure();
    breaker.recordFailure();
    expect(breaker.isOpen()).toBe(false);

    breaker.recordFailure();
    expect(breaker.isOpen()).toBe(true);
    expect(breaker.failures).toBe(3);

    await new Promise((resolve) => setTimeout(resolve, 10));

    expect(breaker.isOpen()).toBe(false);
    expect(breaker.failures).toBe(0);
  });

  test("recordSuccess resets failures", () => {
    const breaker = new CircuitBreaker(2, 1000);
    breaker.recordFailure();
    breaker.recordFailure();
    expect(breaker.isOpen()).toBe(true);

    breaker.recordSuccess();
    expect(breaker.failures).toBe(0);
    expect(breaker.isOpen()).toBe(false);
  });

  test("reset clears state", () => {
    const breaker = new CircuitBreaker(1, 1000);
    breaker.recordFailure();
    expect(breaker.isOpen()).toBe(true);

    breaker.reset();
    expect(breaker.failures).toBe(0);
    expect(breaker.isOpen()).toBe(false);
  });
});

describe("backoffDelay", () => {
  test("returns jittered delay within 50-100% of computed exponential delay", () => {
    const value = backoffDelay(2, {
      maxRetries: 3,
      baseDelayMs: 100,
      maxDelayMs: 10000,
    });

    expect(value).toBeGreaterThanOrEqual(200);
    expect(value).toBeLessThanOrEqual(400);
  });

  test("respects maxDelay cap before applying jitter", () => {
    const value = backoffDelay(10, {
      maxRetries: 3,
      baseDelayMs: 100,
      maxDelayMs: 300,
    });

    expect(value).toBeGreaterThanOrEqual(150);
    expect(value).toBeLessThanOrEqual(300);
  });
});
