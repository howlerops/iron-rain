import type { SlotName } from '../slots/types.js';
import type { EpisodeSummary } from '../episodes/protocol.js';

const SLOT_ROLES: Record<SlotName, string> = {
  main: `You are Cortex, the primary orchestrator. You analyze tasks, plan approaches, and provide comprehensive responses. You have deep reasoning capability and should think through problems carefully before responding.`,
  explore: `You are Scout, specialized in exploration and research. You excel at finding information, reading files, searching codebases, and understanding patterns. Be concise and fact-oriented.`,
  execute: `You are Forge, specialized in execution. You write code, run commands, and make changes. Be precise, write clean code, and explain what you changed.`,
};

export function buildSystemPrompt(slot: SlotName, custom?: string): string {
  const parts: string[] = [];

  // Role instruction
  parts.push(SLOT_ROLES[slot] ?? SLOT_ROLES.main);

  // Project context
  const cwd = process.cwd();
  parts.push(`Working directory: ${cwd}`);

  // Custom prompt override/append
  if (custom) {
    parts.push(custom);
  }

  return parts.join('\n\n');
}

export function buildEpisodeContext(episodes: EpisodeSummary[], maxTokenBudget = 4000): string {
  if (episodes.length === 0) return '';

  // Estimate ~4 chars per token, walk backwards to fit budget
  let charBudget = maxTokenBudget * 4;
  const included: string[] = [];

  for (let i = episodes.length - 1; i >= 0 && charBudget > 0; i--) {
    const ep = episodes[i];
    const summary = `[${ep.slot}] ${ep.task} → ${ep.status}${ep.result ? ': ' + ep.result.slice(0, 200) : ''}`;
    if (summary.length > charBudget) break;
    charBudget -= summary.length;
    included.unshift(summary);
  }

  if (included.length === 0) return '';
  return `## Previous actions this session\n${included.join('\n')}`;
}

/**
 * GOAL/CONTEXT/OUTPUT structured prompt template.
 */
export function structuredPrompt(opts: {
  goal: string;
  context?: string;
  output?: string;
  episodes?: string;
}): string {
  const parts = [`## GOAL\n${opts.goal}`];
  if (opts.context) parts.push(`## CONTEXT\n${opts.context}`);
  if (opts.episodes) parts.push(opts.episodes);
  if (opts.output) parts.push(`## OUTPUT FORMAT\n${opts.output}`);
  return parts.join('\n\n');
}
