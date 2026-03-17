/**
 * PRD and task execution prompt templates.
 */

export const PRD_SYSTEM_PROMPT = `You are Cortex, a senior software architect generating a Product Requirements Document (PRD).

Given the user's request, generate a comprehensive PRD that includes:
1. **Title**: A clear, concise title for the work
2. **Description**: What needs to be built and why
3. **Requirements**: Specific, measurable requirements
4. **Technical Approach**: How to implement it
5. **Out of Scope**: What this does NOT include

Format as clean markdown. Be specific and actionable.`;

export const TASK_BREAKDOWN_PROMPT = `You are Cortex, breaking down a PRD into executable tasks.

Given the PRD below, generate a JSON array of tasks. Each task should be:
- Small enough to complete in one focused session
- Ordered by dependency (earlier tasks first)
- Specific about what files to create/modify

Respond with ONLY a JSON array in this exact format:
[
  {
    "title": "Task title",
    "description": "What to do",
    "acceptanceCriteria": ["Criterion 1", "Criterion 2"],
    "targetFiles": ["path/to/file.ts"]
  }
]`;

export function buildTaskExecutionPrompt(task: {
  title: string;
  description: string;
  acceptanceCriteria: string[];
  targetFiles?: string[];
}, prdContext: string, priorResults: string[]): string {
  const parts = [
    `## TASK: ${task.title}`,
    `\n${task.description}`,
    `\n### Acceptance Criteria`,
    ...task.acceptanceCriteria.map(c => `- ${c}`),
  ];

  if (task.targetFiles?.length) {
    parts.push(`\n### Target Files`);
    parts.push(...task.targetFiles.map(f => `- ${f}`));
  }

  parts.push(`\n### PRD Context\n${prdContext}`);

  if (priorResults.length > 0) {
    parts.push(`\n### Prior Task Results`);
    for (let i = 0; i < priorResults.length; i++) {
      parts.push(`**Task ${i + 1}:** ${priorResults[i]}`);
    }
  }

  parts.push(`\n### Instructions
Implement this task completely. Write all necessary code changes.
After implementation, briefly describe what was done.`);

  return parts.join('\n');
}

export function buildLoopIterationPrompt(config: {
  want: string;
  completionPromise: string;
  iterationIndex: number;
  maxIterations: number;
  priorActions: string[];
}): string {
  const parts = [
    `## GOAL\n${config.want}`,
    `\n## COMPLETION CONDITION\n"${config.completionPromise}"`,
    `\n## STATUS\nIteration ${config.iterationIndex + 1} of ${config.maxIterations}`,
  ];

  if (config.priorActions.length > 0) {
    parts.push(`\n## PRIOR ITERATIONS`);
    for (let i = 0; i < config.priorActions.length; i++) {
      parts.push(`**Iteration ${i + 1}:** ${config.priorActions[i]}`);
    }
  }

  if (config.priorActions.length >= 3) {
    // Check if stuck
    parts.push(`\n## IMPORTANT
You have been iterating for ${config.priorActions.length} rounds. If prior approaches haven't worked, try a fundamentally different strategy.`);
  }

  parts.push(`\n## INSTRUCTIONS
Make progress toward the goal. Describe what you did and the outcome.`);

  return parts.join('\n');
}

export function buildCompletionCheckPrompt(completionPromise: string, lastResult: string): string {
  return `Evaluate whether this condition is now TRUE:

**Condition:** "${completionPromise}"

**Latest result:** ${lastResult}

Respond with ONLY "TRUE" or "FALSE" followed by a brief explanation.`;
}
