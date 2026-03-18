import { describe, expect, it } from "bun:test";
import {
  DEFAULT_SANDBOX_CONFIG,
  getSandboxExecutor,
  wrapCommandForSandbox,
} from "../../sandbox/index.js";

describe("sandbox", () => {
  it("returns undefined for 'none' backend", () => {
    expect(getSandboxExecutor("none")).toBeUndefined();
  });

  it("returns executor for 'seatbelt' backend", () => {
    const executor = getSandboxExecutor("seatbelt");
    expect(executor).toBeTruthy();
    expect(executor?.backend).toBe("seatbelt");
  });

  it("returns executor for 'docker' backend", () => {
    const executor = getSandboxExecutor("docker");
    expect(executor).toBeTruthy();
    expect(executor?.backend).toBe("docker");
  });

  it("returns executor for 'gvisor' backend", () => {
    const executor = getSandboxExecutor("gvisor");
    expect(executor).toBeTruthy();
    expect(executor?.backend).toBe("gvisor");
  });

  it("wrapCommandForSandbox passes through for 'none'", () => {
    const result = wrapCommandForSandbox("ls", ["-la"], DEFAULT_SANDBOX_CONFIG);
    expect(result.command).toBe("ls");
    expect(result.args).toEqual(["-la"]);
  });

  it("wrapCommandForSandbox wraps for seatbelt", () => {
    const config = { ...DEFAULT_SANDBOX_CONFIG, backend: "seatbelt" as const };
    const result = wrapCommandForSandbox("node", ["index.js"], config);
    expect(result.command).toBe("sandbox-exec");
    expect(result.args[0]).toBe("-f");
  });

  it("wrapCommandForSandbox wraps for docker", () => {
    const config = { ...DEFAULT_SANDBOX_CONFIG, backend: "docker" as const };
    const result = wrapCommandForSandbox("node", ["index.js"], config);
    expect(result.command).toBe("docker");
    expect(result.args).toContain("run");
  });
});
