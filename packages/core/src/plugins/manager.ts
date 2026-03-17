import { HookEmitter } from "./hooks.js";
import { createShellHooks, loadPlugins, type PluginModule } from "./loader.js";

export interface PluginManagerConfig {
  paths?: string[];
  hooks?: Record<string, string>;
}

/**
 * Manages plugin lifecycle and hook emission.
 */
export class PluginManager {
  readonly emitter: HookEmitter;
  private plugins: PluginModule[] = [];
  private config: PluginManagerConfig;

  constructor(config?: PluginManagerConfig) {
    this.emitter = new HookEmitter();
    this.config = config ?? {};
  }

  /**
   * Load all plugins and register shell hooks.
   */
  async loadAll(cwd: string): Promise<void> {
    // Load JS plugins
    this.plugins = await loadPlugins(cwd, this.emitter);

    // Register shell command hooks from config
    if (this.config.hooks) {
      createShellHooks(this.config.hooks, this.emitter);
    }
  }

  /**
   * Deactivate all plugins.
   */
  async unloadAll(): Promise<void> {
    for (const plugin of this.plugins) {
      if (plugin.deactivate) {
        try {
          await plugin.deactivate();
        } catch {
          // Ignore deactivation errors
        }
      }
    }
    this.plugins = [];
    this.emitter.clear();
  }

  /**
   * Get list of loaded plugins.
   */
  getPlugins(): readonly PluginModule[] {
    return this.plugins;
  }

  /**
   * Get count of loaded plugins.
   */
  get pluginCount(): number {
    return this.plugins.length;
  }
}
