/**
 * Tool output summarization — truncates large outputs to preserve context window.
 */

const DEFAULT_MAX_CHARS = 8000; // ~2000 tokens

/**
 * Check if output exceeds the summarization threshold.
 */
export function shouldSummarize(output: string, maxChars = DEFAULT_MAX_CHARS): boolean {
  return output.length > maxChars;
}

/**
 * Truncate output keeping first and last portions with a marker in between.
 */
export function truncateWithContext(
  output: string,
  maxChars = DEFAULT_MAX_CHARS,
): string {
  if (output.length <= maxChars) return output;

  const keepEach = Math.floor((maxChars - 60) / 2); // 60 chars for the marker
  const head = output.slice(0, keepEach);
  const tail = output.slice(-keepEach);
  const omitted = output.length - keepEach * 2;

  return `${head}\n\n[...truncated ${omitted} characters...]\n\n${tail}`;
}
