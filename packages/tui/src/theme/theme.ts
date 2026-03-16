export const ironRainTheme = {
  brand: {
    primary: '#c49b21',    // Howlerops gold
    accent: '#d4b820',     // Bright gold
    gold: '#c4a035',       // Logo gold
    darkGold: '#9a7a18',   // Deep gold
    lightGold: '#d4c066',  // Soft gold
  },
  slots: {
    main: '#c49b21',       // Gold — strategy
    explore: '#d4c066',    // Light gold — research
    execute: '#e8a317',    // Amber — action
  },
  status: {
    success: '#22c55e',
    warning: '#eab308',
    error: '#ef4444',
    info: '#c49b21',
  },
  chrome: {
    border: '#3d3d3d',
    muted: '#737373',
    bg: '#000000',         // Pure black (howlerops dark mode)
    card: '#2d2d2d',
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
