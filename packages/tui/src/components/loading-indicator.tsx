import type { SlotName } from "@howlerops/iron-rain";
import { createSignal, onCleanup } from "solid-js";
import { ironRainTheme, slotColor, slotLabel } from "../theme/theme.js";

export interface LoadingIndicatorProps {
  slot: SlotName;
  startTime: number;
}

const SPINNER_FRAMES = [
  "\u2581",
  "\u2582",
  "\u2583",
  "\u2584",
  "\u2585",
  "\u2586",
  "\u2587",
  "\u2588",
  "\u2587",
  "\u2586",
  "\u2585",
  "\u2584",
  "\u2583",
  "\u2582",
];

const SLOT_VERBS: Record<string, string> = {
  main: "is thinking\u2026",
  explore: "is investigating\u2026",
  execute: "is executing\u2026",
};

export function LoadingIndicator(props: LoadingIndicatorProps) {
  const [frame, setFrame] = createSignal(0);
  const [elapsed, setElapsed] = createSignal(0);

  const timer = setInterval(() => {
    setFrame((f) => (f + 1) % SPINNER_FRAMES.length);
    setElapsed(Math.floor((Date.now() - props.startTime) / 1000));
  }, 80);

  onCleanup(() => clearInterval(timer));

  const color = () => slotColor(props.slot);
  const verb = () => SLOT_VERBS[props.slot] ?? "is thinking\u2026";

  return (
    <box flexDirection="row" gap={1} paddingX={1} marginBottom={1}>
      <text fg={color()}>
        <b>{SPINNER_FRAMES[frame()]}</b>
      </text>
      <text fg={color()}>
        <b>{slotLabel(props.slot)}</b>
      </text>
      <text fg={ironRainTheme.chrome.muted}>{verb()}</text>
      <text fg={ironRainTheme.chrome.dimFg}>
        {`(${elapsed()}s \u00B7 esc to cancel \u00B7 enter to add context)`}
      </text>
    </box>
  );
}
