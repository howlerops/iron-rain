import type { SlotConfig, SlotName, ThinkingLevel } from "@howlerops/iron-rain";
import { SLOT_NAMES } from "@howlerops/iron-rain";
import { For, Show } from "solid-js";
import {
  ironRainTheme,
  slotColor,
  slotDescription,
  slotLabel,
} from "../../theme/theme.js";

export const THINKING_LEVELS: ThinkingLevel[] = [
  "off",
  "low",
  "medium",
  "high",
];

export interface ModelOption {
  provider: string;
  model: string;
}

export interface ModelsSectionProps {
  slots: Record<SlotName, SlotConfig> | undefined;
  cursor: number;
  editing: boolean;
  editCursor: number;
  editingThinking: boolean;
  thinkingCursor: number;
  modelOptions: ModelOption[];
  modelsLoading: boolean;
  providerFilter: string;
  availableProviders: string[];
}

export function ModelsSection(props: ModelsSectionProps) {
  const slotNames = [...SLOT_NAMES] as SlotName[];

  return (
    <box flexDirection="column">
      <text fg={ironRainTheme.chrome.muted}>
        Assign a provider and model to each slot.
      </text>
      <box marginY={0} />
      <For each={slotNames}>
        {(slot, i) => {
          const isActive = () => i() === props.cursor;
          const sc = () => props.slots?.[slot];
          const thinking = () => sc()?.thinkingLevel ?? "off";
          return (
            <box flexDirection="column" paddingX={1}>
              <box flexDirection="row" gap={1}>
                <text
                  fg={isActive() ? slotColor(slot) : ironRainTheme.chrome.dimFg}
                >
                  {isActive() ? "\u25B8" : " "}
                </text>
                <text fg={slotColor(slot)}>
                  <b>{slotLabel(slot).padEnd(8)}</b>
                </text>
                <text fg={ironRainTheme.chrome.fg}>
                  {`${sc()?.provider ?? "\u2014"}/${sc()?.model ?? "\u2014"}`}
                </text>
                <text
                  fg={
                    thinking() !== "off"
                      ? ironRainTheme.brand.primary
                      : ironRainTheme.chrome.dimFg
                  }
                >
                  {thinking()}
                </text>
              </box>
              <box flexDirection="row" paddingX={3}>
                <text fg={ironRainTheme.chrome.dimFg}>
                  {slotDescription(slot)}
                </text>
              </box>
            </box>
          );
        }}
      </For>
      <Show when={props.modelsLoading}>
        <box paddingX={2} marginTop={1}>
          <text fg={ironRainTheme.chrome.dimFg}>Loading models...</text>
        </box>
      </Show>

      <Show when={props.editing}>
        <box flexDirection="column" marginTop={1} paddingX={2}>
          <text fg={slotColor(slotNames[props.cursor]!)}>
            <b>{`Select model for ${slotLabel(slotNames[props.cursor]!)}:`}</b>
          </text>
          <box flexDirection="row" gap={1} marginBottom={0}>
            <For each={["all", ...props.availableProviders]}>
              {(filter) => (
                <text
                  fg={
                    filter === props.providerFilter
                      ? ironRainTheme.brand.primary
                      : ironRainTheme.chrome.muted
                  }
                >
                  {filter === props.providerFilter ? (
                    <b>{filter === "all" ? "All" : filter}</b>
                  ) : filter === "all" ? (
                    "All"
                  ) : (
                    filter
                  )}
                </text>
              )}
            </For>
          </box>
          <For each={props.modelOptions}>
            {(option, i) => {
              const isSel = () => i() === props.editCursor;
              return (
                <box flexDirection="row" gap={1} paddingX={1}>
                  <text
                    fg={
                      isSel()
                        ? ironRainTheme.brand.primary
                        : ironRainTheme.chrome.dimFg
                    }
                  >
                    {isSel() ? "\u25B8" : " "}
                  </text>
                  <text
                    fg={
                      isSel()
                        ? ironRainTheme.chrome.fg
                        : ironRainTheme.chrome.muted
                    }
                  >
                    {isSel() ? (
                      <b>{`${option.provider}/${option.model}`}</b>
                    ) : (
                      `${option.provider}/${option.model}`
                    )}
                  </text>
                </box>
              );
            }}
          </For>
          <box flexDirection="row" gap={1} marginTop={0}>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[Enter]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>select</text>
            <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[←→]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>filter</text>
            <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[Esc]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>cancel</text>
          </box>
        </box>
      </Show>

      <Show when={props.editingThinking}>
        <box flexDirection="column" marginTop={1} paddingX={2}>
          <text fg={slotColor(slotNames[props.cursor]!)}>
            <b>{`Thinking level for ${slotLabel(slotNames[props.cursor]!)}:`}</b>
          </text>
          <For each={THINKING_LEVELS}>
            {(level, i) => {
              const isSel = () => i() === props.thinkingCursor;
              return (
                <box flexDirection="row" gap={1} paddingX={1}>
                  <text
                    fg={
                      isSel()
                        ? ironRainTheme.brand.primary
                        : ironRainTheme.chrome.dimFg
                    }
                  >
                    {isSel() ? "\u25B8" : " "}
                  </text>
                  <text
                    fg={
                      isSel()
                        ? ironRainTheme.chrome.fg
                        : ironRainTheme.chrome.muted
                    }
                  >
                    {isSel() ? <b>{level}</b> : level}
                  </text>
                </box>
              );
            }}
          </For>
          <box flexDirection="row" gap={1} marginTop={0}>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[Enter]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>select</text>
            <text fg={ironRainTheme.chrome.dimFg}>{"\u00B7"}</text>
            <text fg={ironRainTheme.chrome.muted}>
              <b>[Esc]</b>
            </text>
            <text fg={ironRainTheme.chrome.dimFg}>cancel</text>
          </box>
        </box>
      </Show>
    </box>
  );
}
