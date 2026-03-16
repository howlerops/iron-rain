export const ironRainTheme = {
  brand: {
    primary: '#7928ca',
    accent: '#ff0080',
  },
  slots: {
    main: '#00b4d8',
    explore: '#90e0ef',
    execute: '#f77f00',
  },
  status: {
    success: '#22c55e',
    warning: '#eab308',
    error: '#ef4444',
    info: '#06b6d4',
  },
  chrome: {
    border: '#4a4a4a',
    muted: '#888888',
    bg: '#1a1a2e',
    fg: '#e0e0e0',
    dimFg: '#666666',
  },
} as const;

export type Theme = typeof ironRainTheme;

export function slotColor(slot: 'main' | 'explore' | 'execute'): string {
  return ironRainTheme.slots[slot];
}

export function slotLabel(slot: 'main' | 'explore' | 'execute'): string {
  const labels = {
    main: 'Main',
    explore: 'Explore',
    execute: 'Execute',
  };
  return labels[slot];
}
