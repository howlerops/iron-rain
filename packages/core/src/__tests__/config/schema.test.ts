import { describe, expect, test } from "bun:test";

import { parseConfig, resolveEnvValue } from "../../config/schema.js";

describe("config schema", () => {
  test("parseConfig accepts valid input", () => {
    const config = parseConfig({
      slots: {
        main: { provider: "openai", model: "gpt-4" },
        explore: { provider: "openai", model: "gpt-4-mini" },
        execute: { provider: "openai", model: "gpt-4-mini" },
      },
      mcpServers: {
        demo: {
          command: "node",
          args: ["server.js"],
        },
      },
    });

    expect(config.slots?.main.model).toBe("gpt-4");
    expect(config.mcpServers?.demo.command).toBe("node");
  });

  test("parseConfig throws on invalid input", () => {
    expect(() =>
      parseConfig({
        slots: {
          main: { provider: "openai", model: "gpt-4", apiBase: "not-a-url" },
          explore: { provider: "openai", model: "gpt-4-mini" },
          execute: { provider: "openai", model: "gpt-4-mini" },
        },
      }),
    ).toThrow();
  });

  test("resolveEnvValue resolves env: prefix", () => {
    process.env.TEST_SECRET = "abc123";
    expect(resolveEnvValue("env:TEST_SECRET")).toBe("abc123");
  });

  test("resolveEnvValue returns empty string for missing env var", () => {
    delete process.env.MISSING_SECRET;
    expect(resolveEnvValue("env:MISSING_SECRET")).toBe("");
  });

  test("resolveEnvValue returns literal values unchanged", () => {
    expect(resolveEnvValue("plain-value")).toBe("plain-value");
  });

  test("applies defaults for context/mcp/resilience sections", () => {
    const config = parseConfig({
      context: {},
      mcp: {},
      resilience: {},
    });

    expect(config.context).toEqual({
      hotWindowSize: 6,
      maxContextTokens: 8000,
      maxFileSize: 102400,
      maxImageSize: 20971520,
      toolOutputMaxTokens: 2000,
    });
    expect(config.mcp).toEqual({
      requestTimeoutMs: 10000,
    });
    expect(config.resilience).toEqual({
      circuitBreakerThreshold: 5,
      circuitBreakerResetMs: 60000,
      maxRetries: 3,
    });
  });

  test("accepts new Phase 1-4 config sections", () => {
    const config = parseConfig({
      autoCommit: { enabled: true, messagePrefix: "auto:" },
      sandbox: { backend: "seatbelt", allowNetwork: false },
      rules: { disabled: false },
      session: { autoResume: true, maxHistory: 100 },
      memory: { autoLearn: true, maxLessons: 25 },
      repoMap: { enabled: true, maxTokens: 3000 },
      plugins: { hooks: { onCommit: "npm run lint" } },
      costs: { "custom-model": { input: 1, output: 2 } },
      lsp: { enabled: true },
      voice: { enabled: false, engine: "whisper" },
      configUrl: "https://example.com/config.json",
    });

    expect(config.autoCommit?.enabled).toBe(true);
    expect(config.sandbox?.backend).toBe("seatbelt");
    expect(config.rules?.disabled).toBe(false);
    expect(config.session?.maxHistory).toBe(100);
    expect(config.memory?.maxLessons).toBe(25);
    expect(config.repoMap?.maxTokens).toBe(3000);
    expect(config.plugins?.hooks?.onCommit).toBe("npm run lint");
    expect(config.costs?.["custom-model"]?.input).toBe(1);
    expect(config.lsp?.enabled).toBe(true);
    expect(config.voice?.engine).toBe("whisper");
    expect(config.configUrl).toBe("https://example.com/config.json");
  });

  test("accepts gvisor and docker sandbox backends", () => {
    const config1 = parseConfig({ sandbox: { backend: "docker" } });
    expect(config1.sandbox?.backend).toBe("docker");

    const config2 = parseConfig({ sandbox: { backend: "gvisor" } });
    expect(config2.sandbox?.backend).toBe("gvisor");
  });
});
