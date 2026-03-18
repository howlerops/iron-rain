/**
 * Built-in code review — analyzes diffs via the main slot.
 */

import { execSync } from "node:child_process";

export interface ReviewResult {
  summary: string;
  issues: ReviewIssue[];
  raw: string;
}

export interface ReviewIssue {
  severity: "critical" | "warning" | "suggestion";
  message: string;
  file?: string;
  line?: number;
}

const REVIEW_PROMPT = `You are a senior code reviewer. Analyze the following code changes for:
1. Bugs and logic errors
2. Security vulnerabilities
3. Performance issues
4. Style and readability problems
5. Missing error handling
6. Missing tests

Format your response as:
## Summary
<1-2 sentence overview>

## Issues
For each issue, use this format:
- **[CRITICAL/WARNING/SUGGESTION]** <file>:<line> — <description>

If no issues found, say "No issues found."`;

/**
 * Get the staged diff for review.
 */
export function getStagedDiff(): string {
  try {
    return execSync("git diff --staged", { encoding: "utf-8", timeout: 10000 });
  } catch {
    return "";
  }
}

/**
 * Get the diff between current branch and a target branch.
 */
export function getBranchDiff(targetBranch: string): string {
  try {
    return execSync(`git diff ${targetBranch}...HEAD`, {
      encoding: "utf-8",
      timeout: 10000,
    });
  } catch {
    return "";
  }
}

/**
 * Build a review prompt from a diff.
 */
export function buildReviewPrompt(diff: string): string {
  if (!diff.trim()) {
    return "No changes to review.";
  }

  // Truncate very large diffs
  const maxChars = 50000;
  const truncated =
    diff.length > maxChars
      ? `${diff.slice(0, maxChars)}\n\n[...diff truncated...]`
      : diff;

  return `${REVIEW_PROMPT}\n\n## Changes\n\`\`\`diff\n${truncated}\n\`\`\``;
}

/**
 * Parse review response into structured result.
 */
export function parseReviewResponse(response: string): ReviewResult {
  const issues: ReviewIssue[] = [];

  const lines = response.split("\n");
  for (const line of lines) {
    const match = line.match(
      /\*\*\[(CRITICAL|WARNING|SUGGESTION)\]\*\*\s*(?:(\S+?)(?::(\d+))?\s*—\s*)?(.+)/i,
    );
    if (match) {
      issues.push({
        severity: match[1].toLowerCase() as ReviewIssue["severity"],
        file: match[2] || undefined,
        line: match[3] ? parseInt(match[3], 10) : undefined,
        message: match[4].trim(),
      });
    }
  }

  // Extract summary section
  const summaryMatch = response.match(/## Summary\n([\s\S]*?)(?=\n## |$)/);
  const summary = summaryMatch?.[1]?.trim() ?? "";

  return { summary, issues, raw: response };
}
