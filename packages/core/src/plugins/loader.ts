import { existsSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import type { HookEmitter, HookEvent, HookHandler } from "./hooks.js";

const PLUGIN_DIR = ".iron-rain/plugins";

export interface PluginModule {
  name: string;
  hooks?: Partial<Record<HookEvent, HookHandler>>;
  activate?: () => void | Promise<void>;
  deactivate?: () => void | Promise<void>;
}

/**
 * Discover and load plugins from the .iron-rain/plugins directory.
 */
export async function loadPlugins(
  cwd: string,
  emitter: HookEmitter,
): Promise<PluginModule[]> {
  const pluginDir = resolve(cwd, PLUGIN_DIR);
  if (!existsSync(pluginDir)) return [];

  const files = readdirSync(pluginDir).filter(
    (f) => f.endsWith(".js") || f.endsWith(".mjs"),
  );

  const loaded: PluginModule[] = [];

  for (const file of files) {
    try {
      const pluginPath = join(pluginDir, file);
      const mod = (await import(pluginPath)) as {
        default?: PluginModule;
      } & Partial<PluginModule>;
      const plugin = mod.default ?? mod;

      if (!plugin.name) {
        plugin.name = file.replace(/\.(js|mjs)$/, "");
      }

      // Register hooks
      if (plugin.hooks) {
        for (const [event, handler] of Object.entries(plugin.hooks)) {
          if (handler) {
            emitter.on(event as HookEvent, handler);
          }
        }
      }

      // Call activate lifecycle
      if (plugin.activate) {
        await plugin.activate();
      }

      loaded.push(plugin as PluginModule);
    } catch (err) {
      process.stderr.write(
        `[plugins] Failed to load ${file}: ${err instanceof Error ? err.message : String(err)}\n`,
      );
    }
  }

  return loaded;
}

/**
 * Create a shell command hook runner.
 */
export function createShellHooks(
  hooks: Record<string, string>,
  emitter: HookEmitter,
): void {
  const { execSync } =
    require("node:child_process") as typeof import("node:child_process");

  for (const [event, command] of Object.entries(hooks)) {
    emitter.on(event as HookEvent, async (data) => {
      try {
        execSync(command, {
          stdio: "pipe",
          env: {
            ...process.env,
            IRON_RAIN_EVENT: data.event,
            IRON_RAIN_PAYLOAD: JSON.stringify(data.payload),
          },
          timeout: 30_000,
        });
      } catch (err) {
        process.stderr.write(
          `[hooks] Shell hook "${command}" failed: ${err instanceof Error ? err.message : String(err)}\n`,
        );
      }
    });
  }
}
