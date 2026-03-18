/**
 * Dispatch tag parser for the orchestrator-as-agent DSL.
 *
 * Cortex (main slot) can emit XML dispatch tags to route work
 * to Scout (explore) or Forge (execute) during execution:
 *
 *   <dispatch slot="explore">Find all test files for the auth module</dispatch>
 *   <dispatch slot="execute">Add error handling to the login function</dispatch>
 *
 * Multiple tags run in parallel. The TUI dispatch loop detects these,
 * executes them via the kernel, and feeds results back to Cortex.
 */

import type { SlotName } from "../slots/types.js";

export interface DispatchTag {
  slot: Extract<SlotName, "explore" | "execute">;
  content: string;
}

const DISPATCH_RE =
  /<dispatch\s+slot="(explore|execute)">([\s\S]*?)<\/dispatch>/g;

/**
 * Parse dispatch tags from Cortex's streaming output.
 * Returns an array of dispatch directives (slot + prompt content).
 */
export function parseDispatchTags(text: string): DispatchTag[] {
  const tags: DispatchTag[] = [];
  // Reset lastIndex for global regex
  DISPATCH_RE.lastIndex = 0;

  let match: RegExpExecArray | null = DISPATCH_RE.exec(text);
  while (match !== null) {
    const slot = match[1] as DispatchTag["slot"];
    const content = match[2].trim();
    if (content.length > 0) {
      tags.push({ slot, content });
    }
    match = DISPATCH_RE.exec(text);
  }

  return tags;
}

/**
 * Strip dispatch tags from text, leaving only the non-dispatch content.
 * Used to clean Cortex output before displaying to the user.
 */
export function stripDispatchTags(text: string): string {
  DISPATCH_RE.lastIndex = 0;
  return text.replace(DISPATCH_RE, "").trim();
}

/**
 * Check whether text contains any dispatch tags.
 * Lighter than full parsing when you only need a boolean check.
 */
export function hasDispatchTags(text: string): boolean {
  DISPATCH_RE.lastIndex = 0;
  return DISPATCH_RE.test(text);
}
