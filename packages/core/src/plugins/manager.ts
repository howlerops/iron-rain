import type { QualityGate } from "../rules/hook-parser.js";
import { parseAllMarkdownHooks } from "../rules/hook-parser.js";
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
  private _qualityGates: QualityGate[] = [];

  constructor(config?: PluginManagerConfig) {
    this.emitter = new HookEmitter();
    this.config = config ?? {};
  }

  /**
   * Load all plugins and register shell hooks.
   * Also parses markdown rule files for hook definitions and quality gates.
   */
  async loadAll(cwd: string, rules?: string[]): Promise<void> {
    // Load JS plugins
    this.plugins = await loadPlugins(cwd, this.emitter);

    // Register shell command hooks from config
    if (this.config.hooks) {
      createShellHooks(this.config.hooks, this.emitter);
    }

    // Parse and register hooks from markdown rules (CLAUDE.md, IRON-RAIN.md, etc.)
    if (rules && rules.length > 0) {
      this.loadMarkdownHooks(rules);
    }
  }

  /**
   * Parse markdown rule contents and register discovered hooks + quality gates.
   */
  loadMarkdownHooks(ruleContents: string[]): void {
    const { hooks, qualityGates } = parseAllMarkdownHooks(ruleContents);

    // Register parsed hooks as shell hooks
    if (hooks.length > 0) {
      const hookMap: Record<string, string> = {};
      for (const h of hooks) {
        // If multiple commands for the same event, chain with &&
        if (hookMap[h.event]) {
          hookMap[h.event] += ` && ${h.command}`;
        } else {
          hookMap[h.event] = h.command;
        }
      }
      createShellHooks(hookMap, this.emitter);
    }

    // Register quality gates as shell hooks on their trigger events
    for (const gate of qualityGates) {
      createShellHooks({ [gate.trigger]: gate.command }, this.emitter);
    }

    this._qualityGates = qualityGates;
  }

  /**
   * Get quality gates extracted from markdown rules.
   */
  get qualityGates(): readonly QualityGate[] {
    return this._qualityGates;
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
