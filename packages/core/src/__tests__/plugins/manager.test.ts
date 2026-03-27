import { describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PluginManager } from "../../plugins/manager.js";

const TMP = join(tmpdir(), `iron-rain-test-plugins-${Date.now()}`);

function setup() {
  mkdirSync(TMP, { recursive: true });
}

function cleanup() {
  if (existsSync(TMP)) rmSync(TMP, { recursive: true });
}

describe("PluginManager", () => {
  it("creates with a HookEmitter", () => {
    const mgr = new PluginManager();
    expect(mgr.emitter).toBeTruthy();
    expect(mgr.pluginCount).toBe(0);
  });

  it("getPlugins returns empty array initially", () => {
    const mgr = new PluginManager();
    expect(mgr.getPlugins()).toEqual([]);
  });

  it("loadAll does not throw when no plugins directory exists", async () => {
    setup();
    try {
      const mgr = new PluginManager();
      await mgr.loadAll(TMP);
      expect(mgr.pluginCount).toBe(0);
    } finally {
      cleanup();
    }
  });

  it("registers shell hooks from config", async () => {
    setup();
    try {
      const mgr = new PluginManager({
        hooks: { onSessionStart: 'echo "started"' },
      });
      await mgr.loadAll(TMP);
      expect(mgr.emitter.listenerCount("onSessionStart")).toBeGreaterThan(0);
    } finally {
      cleanup();
    }
  });

  it("unloadAll clears plugins and emitter", async () => {
    setup();
    try {
      const mgr = new PluginManager({
        hooks: { onCommit: "echo commit" },
      });
      await mgr.loadAll(TMP);
      expect(mgr.emitter.listenerCount("onCommit")).toBeGreaterThan(0);

      await mgr.unloadAll();
      expect(mgr.pluginCount).toBe(0);
      expect(mgr.emitter.listenerCount("onCommit")).toBe(0);
    } finally {
      cleanup();
    }
  });

  it("loads JS plugin from .iron-rain/plugins/", async () => {
    setup();
    try {
      const pluginDir = join(TMP, ".iron-rain", "plugins");
      mkdirSync(pluginDir, { recursive: true });
      writeFileSync(
        join(pluginDir, "test-plugin.js"),
        `module.exports = {
					name: "test-plugin",
					hooks: {},
					activate() {},
					deactivate() {},
				};`,
      );

      const mgr = new PluginManager();
      await mgr.loadAll(TMP);
      expect(mgr.pluginCount).toBe(1);
      expect(mgr.getPlugins()[0].name).toBe("test-plugin");
    } finally {
      cleanup();
    }
  });

  it("plugin hooks are registered on emitter", async () => {
    setup();
    try {
      const pluginDir = join(TMP, ".iron-rain", "plugins");
      mkdirSync(pluginDir, { recursive: true });
      writeFileSync(
        join(pluginDir, "hook-plugin.js"),
        `module.exports = {
					name: "hook-plugin",
					hooks: {
						beforeDispatch(data) {},
						afterDispatch(data) {},
					},
				};`,
      );

      const mgr = new PluginManager();
      await mgr.loadAll(TMP);
      expect(mgr.emitter.listenerCount("beforeDispatch")).toBeGreaterThan(0);
      expect(mgr.emitter.listenerCount("afterDispatch")).toBeGreaterThan(0);
    } finally {
      cleanup();
    }
  });
});
