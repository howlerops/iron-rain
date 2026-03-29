import { render } from "@opentui/solid";
import type { AppProps } from "./app.js";
import { App } from "./app.js";

/**
 * Launch the TUI. This is compiled with the solid plugin so that App
 * is invoked as a proper SolidJS component with reactive lifecycle
 * (onMount, useKeyboard, etc.).
 */
export async function startTUI(props: AppProps = {}): Promise<void> {
  await render(() => <App {...props} />, {
    targetFps: 60,
    exitOnCtrlC: false,
    useMouse: true,
  });
}
