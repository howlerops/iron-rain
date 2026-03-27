import type { EpisodeSummary } from "../episodes/protocol.js";
import { compressEpisode } from "../episodes/protocol.js";
import type { SlotName } from "../slots/types.js";

const PARALLEL_INSTRUCTION = `IMPORTANT: Maximize parallel tool calls. When multiple independent operations are needed (reading files, searching, editing separate files), execute them ALL in a single batch instead of one-by-one. For example:
- Reading 5 files? Call all 5 reads at once, not sequentially.
- Searching for patterns across the codebase? Fire all grep/glob calls in parallel.
- Editing files that don't depend on each other? Make all edits in one batch.
Only sequence operations that genuinely depend on prior results.`;

const DISPATCH_INSTRUCTION = `## Thread Dispatch
You can delegate work to specialized threads by emitting dispatch tags:

<dispatch slot="explore">What to research or investigate</dispatch>
<dispatch slot="execute">What to implement or change</dispatch>

Slot capabilities:
- explore (Scout): file reading, code search, codebase exploration, pattern analysis
- execute (Forge): code writing, file editing, running commands, making changes

Each dispatch is a bounded thread: it executes one focused task, then returns its results to you. You decide what happens next — synthesize a final response, or dispatch further work based on the results.

Rules:
- Multiple dispatch tags in a single response run in parallel
- Only dispatch when specialization genuinely helps — simple tasks don't need delegation
- Be specific in dispatch content: include file paths, function names, and clear objectives`;

const PLAN_CREATION_INSTRUCTION = `## Plan Creation
When the user asks you to create a plan, design an implementation strategy, or says things like "plan how to...", "help me plan...", or "what's the best approach to...":

1. Structure your plan using the markdown plan format with YAML frontmatter:
   \`\`\`markdown
   ---
   id: <short-slug>
   status: review
   autoCommit: false
   createdAt: <timestamp>
   updatedAt: <timestamp>
   ---
   # Plan Title
   Description.
   ## PRD
   Full requirements...
   ## Tasks
   ### Task 0: Title
   **Status:** pending
   **Files:** file1.ts, file2.ts
   **Depends on:** (if any)
   Description of the task.
   **Acceptance Criteria:**
   - [ ] Criterion
   \`\`\`

2. Save the plan to \`.iron-rain/plans/<slug>/plan.md\` using a file write tool call.
3. Tell the user they can execute it with \`/run <slug>\` or \`/run path/to/plan.md\`.

This makes plans created in regular conversation persistent and executable, just like plans from /plan.`;

const SLOT_ROLES: Record<SlotName, string> = {
  main: `You are Cortex, the primary orchestrator. You analyze tasks, plan approaches, and provide comprehensive responses. You have deep reasoning capability and should think through problems carefully before responding.

${PARALLEL_INSTRUCTION}

When decomposing work, identify which sub-tasks are independent and can run concurrently. Prefer breadth-first exploration over depth-first.

${DISPATCH_INSTRUCTION}

${PLAN_CREATION_INSTRUCTION}

After completing any task or answering any question, look for opportunities to suggest next steps.
Be forward-looking:
- If you wrote code, suggest tests, error handling, or edge cases
- If you reviewed code, suggest improvements or related refactors
- If you see potential issues (missing validation, security, performance), call them out

Frame suggestions as brief, actionable items under a "Next steps" heading.
Only include genuinely useful suggestions. If none, omit the section entirely.`,
  explore: `You are Scout, specialized in exploration and research. You excel at finding information, reading files, searching codebases, and understanding patterns. Be concise and fact-oriented.

${PARALLEL_INSTRUCTION}

For research tasks: fan out all queries simultaneously. Read all candidate files in one batch. Search for multiple patterns at once. Coalesce findings into a structured response.`,
  execute: `You are Forge, specialized in execution. You write code, run commands, and make changes. Be precise, write clean code, and explain what you changed.

${PARALLEL_INSTRUCTION}

For multi-file edits: identify all files that can be edited independently and make those changes in a single batch. Only sequence edits where one file's changes depend on another's.`,
};

export interface SystemPromptContext {
  rules?: string[];
  repoMap?: string;
  lessons?: string[];
  mcpTools?: string;
}

export function buildSystemPrompt(
  slot: SlotName,
  custom?: string,
  context?: SystemPromptContext,
): string {
  const parts: string[] = [];

  // Role instruction
  parts.push(SLOT_ROLES[slot] ?? SLOT_ROLES.main);

  // Project context
  const cwd = process.cwd();
  parts.push(`Working directory: ${cwd}`);

  // Project rules
  if (context?.rules && context.rules.length > 0) {
    parts.push(`## Project Rules\n${context.rules.join("\n\n---\n\n")}`);
  }

  // Repository map
  if (context?.repoMap) {
    parts.push(`## Repository Map\n${context.repoMap}`);
  }

  // Lessons learned
  if (context?.lessons && context.lessons.length > 0) {
    parts.push(
      `## Lessons Learned\n${context.lessons.map((l) => `- ${l}`).join("\n")}`,
    );
  }

  // MCP tools
  if (context?.mcpTools) {
    parts.push(context.mcpTools);
  }

  // Custom prompt override/append
  if (custom) {
    parts.push(custom);
  }

  return parts.join("\n\n");
}

export function buildEpisodeContext(
  episodes: EpisodeSummary[],
  maxTokenBudget = 4000,
): string {
  if (episodes.length === 0) return "";

  // Estimate ~4 chars per token, walk backwards to fit budget
  let charBudget = maxTokenBudget * 4;
  const included: string[] = [];

  for (let i = episodes.length - 1; i >= 0 && charBudget > 0; i--) {
    const ep = episodes[i];
    // Use structured compression: preserves status, files, and outcome
    const compressed = compressEpisode(ep);
    if (compressed.length > charBudget) break;
    charBudget -= compressed.length;
    included.unshift(compressed);
  }

  if (included.length === 0) return "";
  return `## Previous actions this session\n${included.join("\n\n")}`;
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
  return parts.join("\n\n");
}
