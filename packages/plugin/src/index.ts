import type {
  EpisodeSummary,
  IronRainConfig,
  OrchestratorTask,
  SlotConfig,
  SlotName,
  ToolType,
} from "@howlerops/iron-rain";

// Re-export core types for plugin authors
export type {
  EpisodeSummary,
  IronRainConfig,
  OrchestratorTask,
  SlotConfig,
  SlotName,
  ToolType,
};

export interface PluginContext {
  config: IronRainConfig;
  getSlot(name: SlotName): SlotConfig;
  dispatch(task: OrchestratorTask): Promise<EpisodeSummary>;
}

export interface PluginHooks {
  onInit?(ctx: PluginContext): void | Promise<void>;
  onBeforeDispatch?(
    task: OrchestratorTask,
    ctx: PluginContext,
  ): OrchestratorTask | undefined;
  onAfterDispatch?(episode: EpisodeSummary, ctx: PluginContext): void;
  onSlotChange?(slot: SlotName, config: SlotConfig, ctx: PluginContext): void;
  onDestroy?(ctx: PluginContext): void | Promise<void>;
}

export interface PluginDefinition {
  name: string;
  version: string;
  description?: string;
  hooks: PluginHooks;
}

export function definePlugin(def: PluginDefinition): PluginDefinition {
  return def;
}
