import type { ToolType } from '../slots/types.js';

/**
 * Heuristic patterns to detect what kind of tool/action a prompt is requesting.
 * Returns the most likely ToolType, or null if no strong signal.
 */

interface HeuristicRule {
  toolType: ToolType;
  patterns: RegExp[];
  weight: number;
}

const RULES: HeuristicRule[] = [
  // Exploration tasks → explore slot (Scout)
  {
    toolType: 'search',
    patterns: [
      /\b(find|search|look\s+for|locate|where\s+is|grep|rg)\b/i,
      /\b(what\s+files?|which\s+files?|show\s+me\s+files?)\b/i,
    ],
    weight: 2,
  },
  {
    toolType: 'read',
    patterns: [
      /\b(read|show|display|cat|view|open)\s+(the\s+)?(file|code|source|content)/i,
      /\b(what'?s?\s+in|look\s+at|examine|inspect)\b/i,
    ],
    weight: 2,
  },
  {
    toolType: 'glob',
    patterns: [
      /\b(list|ls|directory|folder|tree|structure)\b/i,
      /\b(file\s+structure|project\s+layout)\b/i,
    ],
    weight: 1,
  },

  // Execution tasks → execute slot (Forge)
  {
    toolType: 'edit',
    patterns: [
      /\b(edit|modify|change|update|fix|refactor|rename|replace)\b/i,
      /\b(add\s+a?\s*(function|method|class|component|endpoint|route|test))\b/i,
    ],
    weight: 2,
  },
  {
    toolType: 'write',
    patterns: [
      /\b(create|write|generate|scaffold|make)\s+(a\s+)?(new\s+)?(file|script|module|component)/i,
      /\b(implement|build|code)\b/i,
    ],
    weight: 2,
  },
  {
    toolType: 'bash',
    patterns: [
      /\b(run|execute|install|build|test|deploy|npm|bun|yarn|pip|cargo)\b/i,
      /\b(terminal|command|shell|script)\b/i,
    ],
    weight: 1,
  },

  // Strategy/planning → main slot (Cortex)
  {
    toolType: 'strategy',
    patterns: [
      /\b(explain|describe|why|how\s+does|what\s+is|compare|analyze|review)\b/i,
      /\b(architecture|design|approach|strategy|trade-?off)\b/i,
    ],
    weight: 1,
  },
  {
    toolType: 'plan',
    patterns: [
      /\b(plan|outline|roadmap|steps?\s+to|break\s+down)\b/i,
    ],
    weight: 2,
  },
];

export function detectToolType(prompt: string): ToolType | null {
  const scores = new Map<ToolType, number>();

  for (const rule of RULES) {
    for (const pattern of rule.patterns) {
      if (pattern.test(prompt)) {
        scores.set(rule.toolType, (scores.get(rule.toolType) ?? 0) + rule.weight);
      }
    }
  }

  if (scores.size === 0) return null;

  // Find highest scoring tool type
  let best: ToolType | null = null;
  let bestScore = 0;
  for (const [toolType, score] of scores) {
    if (score > bestScore) {
      bestScore = score;
      best = toolType;
    }
  }

  // Only return if we have a clear signal (score >= 2)
  return bestScore >= 2 ? best : null;
}
