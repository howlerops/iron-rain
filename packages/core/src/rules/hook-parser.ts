/**
 * Parse hook definitions and quality gate rules from markdown files
 * (CLAUDE.md, IRON-RAIN.md, agents.md, README.md, etc.).
 *
 * Supports two formats:
 *
 * 1. Fenced code blocks with `hooks` language tag:
 *    ```hooks
 *    beforeDispatch: bun run lint
 *    afterDispatch: bun run typecheck
 *    ```
 *
 * 2. ## Hooks / ## Quality Gates sections with key: value lines.
 *
 * Also extracts quality gate directives (lint commands, typecheck commands,
 * test commands) from markdown content.
 */

import type { HookEvent } from "../plugins/hooks.js";

const VALID_HOOK_EVENTS = new Set<string>([
  "onToolCall",
  "onToolResult",
  "onSessionStart",
  "onSessionEnd",
  "onCommit",
  "onError",
  "onCheckpoint",
  "beforeDispatch",
  "afterDispatch",
]);

export interface ParsedHook {
  event: HookEvent;
  command: string;
}

export interface QualityGate {
  type: "lint" | "typecheck" | "test" | "format" | "custom";
  command: string;
  /** When to run: beforeDispatch, afterDispatch, onCommit, etc. */
  trigger: HookEvent;
  label: string;
}

export interface MarkdownHookResult {
  hooks: ParsedHook[];
  qualityGates: QualityGate[];
}

/**
 * Parse hooks and quality gates from a single markdown file's content.
 */
export function parseMarkdownHooks(content: string): MarkdownHookResult {
  const hooks: ParsedHook[] = [];
  const qualityGates: QualityGate[] = [];

  // 1. Parse ```hooks fenced code blocks
  hooks.push(...parseFencedHooks(content));

  // 2. Parse ## Hooks / ## Quality Gates sections
  hooks.push(...parseSectionHooks(content));
  qualityGates.push(...parseSectionQualityGates(content));

  // 3. Extract quality gate directives from general content
  qualityGates.push(...extractQualityGates(content));

  // Deduplicate hooks (same event + command)
  const seen = new Set<string>();
  const uniqueHooks = hooks.filter((h) => {
    const key = `${h.event}:${h.command}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });

  // Deduplicate quality gates
  const gSeen = new Set<string>();
  const uniqueGates = qualityGates.filter((g) => {
    const key = `${g.type}:${g.command}`;
    if (gSeen.has(key)) return false;
    gSeen.add(key);
    return true;
  });

  return { hooks: uniqueHooks, qualityGates: uniqueGates };
}

/**
 * Parse hooks from all markdown rule contents (multiple files).
 */
export function parseAllMarkdownHooks(
  ruleContents: string[],
): MarkdownHookResult {
  const allHooks: ParsedHook[] = [];
  const allGates: QualityGate[] = [];

  for (const content of ruleContents) {
    const result = parseMarkdownHooks(content);
    allHooks.push(...result.hooks);
    allGates.push(...result.qualityGates);
  }

  // Deduplicate
  const seen = new Set<string>();
  const uniqueHooks = allHooks.filter((h) => {
    const key = `${h.event}:${h.command}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });

  const gSeen = new Set<string>();
  const uniqueGates = allGates.filter((g) => {
    const key = `${g.type}:${g.command}`;
    if (gSeen.has(key)) return false;
    gSeen.add(key);
    return true;
  });

  return { hooks: uniqueHooks, qualityGates: uniqueGates };
}

// ── Internal parsers ─────────────────────────────────────────────

/**
 * Parse ```hooks fenced code blocks.
 * Format: one hook per line, "eventName: shell command"
 */
function parseFencedHooks(content: string): ParsedHook[] {
  const hooks: ParsedHook[] = [];
  const fenceRegex = /```hooks\s*\n([\s\S]*?)```/g;

  for (
    let match = fenceRegex.exec(content);
    match !== null;
    match = fenceRegex.exec(content)
  ) {
    const block = match[1];
    hooks.push(...parseHookLines(block));
  }

  return hooks;
}

/**
 * Parse ## Hooks section — looks for a markdown heading containing "hooks"
 * and parses key: value pairs until the next heading or end of content.
 */
function parseSectionHooks(content: string): ParsedHook[] {
  const hooks: ParsedHook[] = [];
  const sectionRegex = /^#{1,3}\s+hooks?\s*$/im;
  const match = sectionRegex.exec(content);
  if (!match) return hooks;

  const afterHeading = content.slice(match.index + match[0].length);
  const nextHeading = afterHeading.search(/^#{1,3}\s+/m);
  const section =
    nextHeading >= 0 ? afterHeading.slice(0, nextHeading) : afterHeading;

  hooks.push(...parseHookLines(section));
  return hooks;
}

/**
 * Parse ## Quality Gates section.
 */
function parseSectionQualityGates(content: string): QualityGate[] {
  const gates: QualityGate[] = [];
  const sectionRegex = /^#{1,3}\s+quality\s+gates?\s*$/im;
  const match = sectionRegex.exec(content);
  if (!match) return gates;

  const afterHeading = content.slice(match.index + match[0].length);
  const nextHeading = afterHeading.search(/^#{1,3}\s+/m);
  const section =
    nextHeading >= 0 ? afterHeading.slice(0, nextHeading) : afterHeading;

  gates.push(...parseQualityGateLines(section));
  return gates;
}

/**
 * Extract quality gates from general markdown content.
 * Looks for patterns like:
 *   - "always run `bun run lint` before committing"
 *   - "run `bun test` after changes"
 *   - "typecheck with `tsc --noEmit`"
 */
function extractQualityGates(content: string): QualityGate[] {
  const gates: QualityGate[] = [];

  // Match "lint: command" or "typecheck: command" or "test: command"
  // in any context (including bullet lists)
  const directiveRegex =
    /(?:^|\n)\s*[-*]?\s*(lint|typecheck|type[- ]?check|test|format)(?:ing)?(?:\s+command)?:\s*`?([^`\n]+)`?/gi;
  for (
    let match = directiveRegex.exec(content);
    match !== null;
    match = directiveRegex.exec(content)
  ) {
    const type = normalizeGateType(match[1]);
    const command = match[2].trim();
    if (type && command) {
      gates.push({
        type,
        command,
        trigger: type === "test" ? "afterDispatch" : "beforeDispatch",
        label: `${type}: ${command}`,
      });
    }
  }

  // Match backtick-wrapped commands after "always run", "run", etc.
  const inlineRegex =
    /(?:always\s+)?run\s+`([^`]+)`\s+(?:before|after|on)\s+(commit|dispatch|changes?|save)/gi;
  for (
    let match = inlineRegex.exec(content);
    match !== null;
    match = inlineRegex.exec(content)
  ) {
    const command = match[1].trim();
    const whenRaw = match[2].toLowerCase();
    const trigger: HookEvent =
      whenRaw === "commit"
        ? "onCommit"
        : whenRaw.startsWith("change") || whenRaw === "save"
          ? "afterDispatch"
          : "beforeDispatch";
    const type = inferGateType(command);
    gates.push({ type, command, trigger, label: `${type}: ${command}` });
  }

  return gates;
}

// ── Helpers ──────────────────────────────────────────────────────

function parseHookLines(text: string): ParsedHook[] {
  const hooks: ParsedHook[] = [];
  const lines = text.split("\n");

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#") || trimmed.startsWith("//"))
      continue;

    // Strip leading "- " for bullet list items
    const cleaned = trimmed.replace(/^[-*]\s*/, "");

    const colonIdx = cleaned.indexOf(":");
    if (colonIdx < 0) continue;

    const event = cleaned.slice(0, colonIdx).trim();
    const command = cleaned
      .slice(colonIdx + 1)
      .trim()
      .replace(/^`|`$/g, "");

    if (VALID_HOOK_EVENTS.has(event) && command) {
      hooks.push({ event: event as HookEvent, command });
    }
  }

  return hooks;
}

function parseQualityGateLines(text: string): QualityGate[] {
  const gates: QualityGate[] = [];
  const lines = text.split("\n");

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;

    const cleaned = trimmed.replace(/^[-*]\s*/, "");
    const colonIdx = cleaned.indexOf(":");
    if (colonIdx < 0) continue;

    const typeRaw = cleaned.slice(0, colonIdx).trim();
    const command = cleaned
      .slice(colonIdx + 1)
      .trim()
      .replace(/^`|`$/g, "");

    const type = normalizeGateType(typeRaw);
    if (type && command) {
      gates.push({
        type,
        command,
        trigger: type === "test" ? "afterDispatch" : "beforeDispatch",
        label: `${type}: ${command}`,
      });
    }
  }

  return gates;
}

function normalizeGateType(
  raw: string,
): "lint" | "typecheck" | "test" | "format" | "custom" | null {
  const lower = raw.toLowerCase().replace(/[-_ ]/g, "");
  if (lower === "lint" || lower === "linting") return "lint";
  if (lower === "typecheck" || lower === "typechecking" || lower === "tsc")
    return "typecheck";
  if (lower === "test" || lower === "testing") return "test";
  if (lower === "format" || lower === "formatting") return "format";
  if (lower === "custom") return "custom";
  return null;
}

function inferGateType(
  command: string,
): "lint" | "typecheck" | "test" | "format" | "custom" {
  const lower = command.toLowerCase();
  if (
    lower.includes("lint") ||
    lower.includes("eslint") ||
    lower.includes("biome check")
  )
    return "lint";
  if (
    lower.includes("tsc") ||
    lower.includes("typecheck") ||
    lower.includes("type-check")
  )
    return "typecheck";
  if (
    lower.includes("test") ||
    lower.includes("jest") ||
    lower.includes("vitest")
  )
    return "test";
  if (
    lower.includes("format") ||
    lower.includes("prettier") ||
    lower.includes("biome format")
  )
    return "format";
  return "custom";
}
