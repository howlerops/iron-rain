import type {
  IronRainConfig,
  SlotConfig,
  SlotName,
} from "@howlerops/iron-rain";
import { ModelRegistry, SLOT_NAMES, writeConfig } from "@howlerops/iron-rain";
import { useKeyboard } from "@opentui/solid";
import {
  createMemo,
  createSignal,
  For,
  Match,
  onMount,
  Switch,
} from "solid-js";
import { createStore } from "solid-js/store";
import { ironRainTheme } from "../theme/theme.js";
import { AVAILABLE_PROVIDERS, PROVIDER_MODELS } from "./onboarding/types.js";
import { AboutSection } from "./settings/about-section.js";
import {
  type ModelOption,
  ModelsSection,
  THINKING_LEVELS,
} from "./settings/models-section.js";
import {
  type ProviderListItem,
  ProvidersSection,
} from "./settings/providers-section.js";

export type SettingsSection = "models" | "providers" | "about";

export interface SettingsProps {
  config: IronRainConfig;
  onSave: (config: IronRainConfig) => void;
  onClose: () => void;
  initialSection?: SettingsSection;
}

const SECTIONS: SettingsSection[] = ["models", "providers", "about"];
const SECTION_LABELS: Record<SettingsSection, string> = {
  models: "Models",
  providers: "Providers",
  about: "About",
};

export function Settings(props: SettingsProps) {
  const [activeSection, setActiveSection] = createSignal<SettingsSection>(
    props.initialSection ?? "models",
  );
  const [cursor, setCursor] = createSignal(0);
  const [editing, setEditing] = createSignal(false);
  const [editCursor, setEditCursor] = createSignal(0);
  const [providerFilter, setProviderFilter] = createSignal<string>("all");
  const [editingKey, setEditingKey] = createSignal(false);
  const [keyBuffer, setKeyBuffer] = createSignal("");
  const [editingThinking, setEditingThinking] = createSignal(false);
  const [thinkingCursor, setThinkingCursor] = createSignal(0);
  const [dynamicModels, setDynamicModels] = createSignal<
    Record<string, string[]>
  >({});
  const [modelsLoading, setModelsLoading] = createSignal(false);

  const registry = new ModelRegistry(PROVIDER_MODELS);
  const slotNames = [...SLOT_NAMES] as SlotName[];

  const [config, setConfig] = createStore<IronRainConfig>(
    JSON.parse(JSON.stringify(props.config)),
  );

  // Fetch dynamic models on mount
  onMount(async () => {
    setModelsLoading(true);
    const providers = config.providers ?? {};
    const fetched: Record<string, string[]> = {};
    await Promise.all(
      ["ollama", "openai", "gemini"]
        .filter((pid) => providers[pid])
        .map(async (pid) => {
          const models = await registry.getModels(pid, providers[pid]);
          if (models.length > 0) fetched[pid] = models;
        }),
    );
    setDynamicModels(fetched);
    setModelsLoading(false);
  });

  const modelOptions = createMemo((): ModelOption[] => {
    const options: ModelOption[] = [];
    const dynamic = dynamicModels();
    const providers = config.providers ?? {};
    for (const pid of Object.keys(providers)) {
      for (const m of dynamic[pid] ?? PROVIDER_MODELS[pid] ?? []) {
        options.push({ provider: pid, model: m });
      }
    }
    if (config.slots) {
      for (const slot of slotNames) {
        const sc = config.slots[slot];
        if (sc) {
          for (const m of dynamic[sc.provider] ??
            PROVIDER_MODELS[sc.provider] ??
            []) {
            if (
              !options.some((o) => o.provider === sc.provider && o.model === m)
            ) {
              options.push({ provider: sc.provider, model: m });
            }
          }
        }
      }
    }
    // Always include CLI providers (no API key required)
    for (const cliProvider of ["claude-code", "codex", "gemini-cli"]) {
      const models = PROVIDER_MODELS[cliProvider] ?? [];
      for (const m of models) {
        if (!options.some((o) => o.provider === cliProvider && o.model === m)) {
          options.push({ provider: cliProvider, model: m });
        }
      }
    }
    return options;
  });

  const availableProviders = createMemo(() => [
    ...new Set(modelOptions().map((o) => o.provider)),
  ]);

  const filteredModelOptions = createMemo(() => {
    const filter = providerFilter();
    if (filter === "all") return modelOptions();
    return modelOptions().filter((o) => o.provider === filter);
  });

  const providerList = createMemo((): ProviderListItem[] => {
    const configured = Object.keys(config.providers ?? {});
    return AVAILABLE_PROVIDERS.map((p) => ({
      ...p,
      configured: configured.includes(p.id),
      apiKey: config.providers?.[p.id]?.apiKey,
      apiBase: config.providers?.[p.id]?.apiBase ?? p.defaultApiBase,
    }));
  });

  function maxCursorForSection(): number {
    switch (activeSection()) {
      case "models":
        return slotNames.length - 1;
      case "providers":
        return providerList().length - 1;
      default:
        return 0;
    }
  }

  function cycleSection(dir: 1 | -1) {
    const idx = SECTIONS.indexOf(activeSection());
    setActiveSection(
      SECTIONS[(idx + dir + SECTIONS.length) % SECTIONS.length]!,
    );
    setCursor(0);
    setEditing(false);
    setEditingKey(false);
  }

  function selectModelForSlot(slotIdx: number, optionIdx: number) {
    const opt = filteredModelOptions()[optionIdx];
    if (!opt) return;
    const slot = slotNames[slotIdx]!;
    setConfig("slots", slot, {
      provider: opt.provider,
      model: opt.model,
      apiKey: config.providers?.[opt.provider]?.apiKey,
      apiBase: config.providers?.[opt.provider]?.apiBase,
    } as SlotConfig);
    setEditing(false);
    setProviderFilter("all");
  }

  function autoAssignSlotsToProvider(providerId: string) {
    const models = PROVIDER_MODELS[providerId] ?? [];
    if (models.length === 0) return;
    const defaultModel = models[0]!;
    const configuredProviders = Object.keys(config.providers ?? {});
    for (const slot of slotNames) {
      const sc = config.slots?.[slot];
      if (!sc || !configuredProviders.includes(sc.provider)) {
        setConfig("slots", slot, {
          provider: providerId,
          model: defaultModel,
          apiBase: config.providers?.[providerId]?.apiBase,
        } as SlotConfig);
      }
    }
  }

  function toggleProvider(idx: number) {
    const prov = providerList()[idx];
    if (!prov) return;
    if (prov.configured) {
      const providers = { ...(config.providers ?? {}) };
      delete providers[prov.id];
      setConfig("providers", providers);
    } else if (prov.requiresKey) {
      setEditingKey(true);
      setKeyBuffer("");
    } else {
      const providers = { ...(config.providers ?? {}) };
      providers[prov.id] = { apiBase: prov.defaultApiBase };
      setConfig("providers", providers);
      autoAssignSlotsToProvider(prov.id);
    }
  }

  function commitApiKey(idx: number) {
    const prov = providerList()[idx];
    if (!prov) return;
    const key = keyBuffer().trim();
    if (!key) {
      setEditingKey(false);
      return;
    }
    const providers = { ...(config.providers ?? {}) };
    providers[prov.id] = {
      apiKey: key,
      apiBase: prov.apiBase ?? prov.defaultApiBase,
    };
    setConfig("providers", providers);
    autoAssignSlotsToProvider(prov.id);
    setEditingKey(false);
    setKeyBuffer("");
  }

  useKeyboard((e) => {
    // API key text entry mode
    if (editingKey()) {
      if (e.name === "return") commitApiKey(cursor());
      else if (e.name === "escape") {
        setEditingKey(false);
        setKeyBuffer("");
      } else if (e.name === "backspace") setKeyBuffer((k) => k.slice(0, -1));
      else if (e.raw && e.raw.length === 1 && !e.ctrl && !e.meta)
        setKeyBuffer((k) => k + e.raw);
      e.preventDefault();
      return;
    }

    // Model picker sub-menu
    if (editing()) {
      if (e.name === "escape") {
        setEditing(false);
        setProviderFilter("all");
      } else if (e.name === "up") setEditCursor((c) => Math.max(0, c - 1));
      else if (e.name === "down")
        setEditCursor((c) =>
          Math.min(filteredModelOptions().length - 1, c + 1),
        );
      else if (e.name === "left" || e.name === "right") {
        const filters = ["all", ...availableProviders()];
        const idx = filters.indexOf(providerFilter());
        const dir = e.name === "right" ? 1 : -1;
        const next = filters[(idx + dir + filters.length) % filters.length]!;
        setProviderFilter(next);
        setEditCursor(0);
      } else if (e.name === "return")
        selectModelForSlot(cursor(), editCursor());
      e.preventDefault();
      return;
    }

    // Thinking level picker
    if (editingThinking()) {
      if (e.name === "escape") setEditingThinking(false);
      else if (e.name === "up") setThinkingCursor((c) => Math.max(0, c - 1));
      else if (e.name === "down")
        setThinkingCursor((c) => Math.min(THINKING_LEVELS.length - 1, c + 1));
      else if (e.name === "return") {
        const slot = slotNames[cursor()]!;
        const level = THINKING_LEVELS[thinkingCursor()]!;
        setConfig(
          "slots",
          slot,
          "thinkingLevel",
          level === "off" ? undefined : level,
        );
        setEditingThinking(false);
      }
      e.preventDefault();
      return;
    }

    // Main settings nav
    if (e.name === "escape") {
      props.onClose();
      e.preventDefault();
      return;
    }
    if (e.name === "tab") {
      cycleSection(e.shift ? -1 : 1);
      e.preventDefault();
      return;
    }
    if (e.name === "up") {
      setCursor((c) => Math.max(0, c - 1));
      e.preventDefault();
      return;
    }
    if (e.name === "down") {
      setCursor((c) => Math.min(maxCursorForSection(), c + 1));
      e.preventDefault();
      return;
    }
    if (e.name === "return") {
      if (activeSection() === "models") {
        setEditing(true);
        setEditCursor(0);
        setProviderFilter("all");
      } else if (activeSection() === "providers") toggleProvider(cursor());
      e.preventDefault();
      return;
    }
    if (e.raw === "t" && activeSection() === "models") {
      setEditingThinking(true);
      const slot = slotNames[cursor()]!;
      setThinkingCursor(
        THINKING_LEVELS.indexOf(config.slots?.[slot]?.thinkingLevel ?? "off"),
      );
      e.preventDefault();
      return;
    }
    if (e.raw === "s") {
      writeConfig(config);
      props.onSave(config);
      e.preventDefault();
    }
  });

  return (
    <box flexDirection="column" paddingX={2} paddingY={1} flexGrow={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>Settings</b>
      </text>

      <box flexDirection="row" gap={2} marginTop={1}>
        <For each={SECTIONS}>
          {(s) => {
            const isActive = () => s === activeSection();
            return (
              <box flexDirection="column">
                <text
                  fg={
                    isActive()
                      ? ironRainTheme.brand.primary
                      : ironRainTheme.chrome.muted
                  }
                >
                  {isActive() ? <b>{SECTION_LABELS[s]}</b> : SECTION_LABELS[s]}
                </text>
                <text
                  fg={isActive() ? ironRainTheme.brand.primary : "transparent"}
                >
                  {"\u2500".repeat(SECTION_LABELS[s].length)}
                </text>
              </box>
            );
          }}
        </For>
      </box>

      <box marginY={1} />

      <Switch>
        <Match when={activeSection() === "models"}>
          <ModelsSection
            slots={config.slots}
            cursor={cursor()}
            editing={editing()}
            editCursor={editCursor()}
            editingThinking={editingThinking()}
            thinkingCursor={thinkingCursor()}
            modelOptions={filteredModelOptions()}
            modelsLoading={modelsLoading()}
            providerFilter={providerFilter()}
            availableProviders={availableProviders()}
          />
        </Match>
        <Match when={activeSection() === "providers"}>
          <ProvidersSection
            providers={providerList()}
            cursor={cursor()}
            editingKey={editingKey()}
            keyBuffer={keyBuffer()}
          />
        </Match>
        <Match when={activeSection() === "about"}>
          <AboutSection />
        </Match>
      </Switch>

      <box flexGrow={1} />
      <box flexDirection="row" gap={1} paddingX={1}>
        <text fg={ironRainTheme.chrome.muted}>
          <b>Tab</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>section</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>{"\u2191\u2193"}</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>navigate</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>Enter</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>
          {activeSection() === "models" ? "edit model" : "toggle"}
        </text>
        {editing() && (
          <>
            <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
            <text fg={ironRainTheme.chrome.muted}>
              <b>{"\u2190\u2192"}</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>filter</text>
          </>
        )}
        {activeSection() === "models" && !editing() && (
          <>
            <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
            <text fg={ironRainTheme.chrome.muted}>
              <b>t</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>thinking</text>
          </>
        )}
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>s</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>save</text>
        <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
        <text fg={ironRainTheme.chrome.muted}>
          <b>Esc</b>
        </text>
        <text fg={ironRainTheme.chrome.dimFg}>close</text>
      </box>
    </box>
  );
}
