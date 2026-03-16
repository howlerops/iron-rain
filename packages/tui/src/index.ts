export { App } from './app.js';
export type { AppProps } from './app.js';

export { SlateProvider, useSlate } from './context/slate-context.js';
export type { SlateState, SlateActions } from './context/slate-context.js';

export { ironRainTheme, slotColor, slotLabel } from './theme/theme.js';
export type { Theme } from './theme/theme.js';
export { SPLASH_ART, TAGLINE } from './theme/splash.js';

export * from './components/index.js';

export { render } from '@opentui/solid';
export { useKeyboard, useRenderer, useTerminalDimensions } from '@opentui/solid';
