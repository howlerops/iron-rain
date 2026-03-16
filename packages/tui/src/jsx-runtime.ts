// Minimal JSX runtime for OpenTUI elements.
// At runtime, a real TUI renderer (OpenTUI) replaces this.
// This exists so TypeScript can compile JSX to function calls.

export function jsx(
  type: string | Function,
  props: Record<string, any>,
  key?: string,
): any {
  return { type, props, key };
}

export const jsxs = jsx;
export const jsxDEV = jsx;

export function Fragment(props: { children?: any }) {
  return props.children;
}
