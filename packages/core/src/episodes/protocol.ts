import type { SlotName } from "../slots/types.js";
import { generateId } from "../utils/id.js";

export interface EpisodeSummary {
  id: string;
  slot: SlotName;
  task: string;
  result: string;
  tokens: number;
  duration: number;
  filesModified?: string[];
  status: "success" | "failure" | "partial";
}

export function createEpisodeSummary(
  partial: Omit<EpisodeSummary, "id">,
): EpisodeSummary {
  return {
    id: generateId(),
    ...partial,
  };
}

/**
 * Compress an episode into a structured summary suitable for context injection.
 * Preserves: slot, status, task summary, files modified, and key outcome.
 * This replaces naive 200-char truncation with meaningful compression.
 */
export function compressEpisode(episode: EpisodeSummary): string {
  const parts: string[] = [];

  const taskSummary = summarizeText(episode.task, 100);
  parts.push(`[${episode.slot}/${episode.status}] ${taskSummary}`);

  if (episode.filesModified?.length) {
    parts.push(`  Files: ${episode.filesModified.join(", ")}`);
  }

  if (episode.result) {
    const outcome = extractOutcome(episode.result, 400);
    parts.push(`  Result: ${outcome}`);
  }

  return parts.join("\n");
}

/**
 * Format episodes as composable input context for thread-to-thread handoffs.
 * This is the mechanism the blog describes: "episodes can be direct inputs
 * to other threads", enabling thread composition where one thread's
 * conclusions become another thread's starting context.
 */
export function formatEpisodeInputs(episodes: EpisodeSummary[]): string {
  if (episodes.length === 0) return "";

  const parts = ["## Prior Thread Episodes"];

  for (let i = 0; i < episodes.length; i++) {
    const ep = episodes[i];
    parts.push(`\n### Episode ${i + 1} [${ep.slot}] \u2014 ${ep.status}`);
    parts.push(`**Task:** ${summarizeText(ep.task, 200)}`);

    if (ep.filesModified?.length) {
      parts.push(`**Files modified:** ${ep.filesModified.join(", ")}`);
    }

    // For input episodes, include more result detail than compressed context
    if (ep.result) {
      const outcome = extractOutcome(ep.result, 800);
      parts.push(`**Result:**\n${outcome}`);
    }
  }

  return parts.join("\n");
}

/**
 * Extract keywords from episode content for RLM retrieval.
 */
export function extractEpisodeKeywords(episode: EpisodeSummary): Set<string> {
  const text = `${episode.task} ${episode.result ?? ""}`;
  return extractKeywords(text);
}

/**
 * Score relevance between a set of query keywords and an episode.
 * Uses keyword overlap (Jaccard-like scoring).
 */
export function episodeRelevance(
  queryKeywords: Set<string>,
  episode: EpisodeSummary,
): number {
  const epKeywords = extractEpisodeKeywords(episode);
  if (queryKeywords.size === 0 || epKeywords.size === 0) return 0;
  let overlap = 0;
  for (const kw of queryKeywords) {
    if (epKeywords.has(kw)) overlap++;
  }
  return overlap / queryKeywords.size;
}

// ── Internal helpers ─────────────────────────────────────────────

function summarizeText(text: string, maxLen: number): string {
  const firstLine = text.split("\n")[0] ?? text;
  const clean = firstLine.replace(/^#+\s+/, "").trim();
  return clean.length > maxLen ? `${clean.slice(0, maxLen - 3)}...` : clean;
}

function extractOutcome(result: string, maxLen: number): string {
  const cleaned = result.replace(/^#+\s+/gm, "").trim();
  const firstPara = cleaned.split(/\n\n/)[0] ?? cleaned;
  return firstPara.length > maxLen
    ? `${firstPara.slice(0, maxLen - 3)}...`
    : firstPara;
}

// Minimal stop words for keyword extraction
const STOP_WORDS = new Set([
  "the",
  "a",
  "an",
  "is",
  "are",
  "was",
  "were",
  "be",
  "been",
  "being",
  "have",
  "has",
  "had",
  "do",
  "does",
  "did",
  "will",
  "would",
  "could",
  "should",
  "may",
  "might",
  "shall",
  "can",
  "need",
  "must",
  "that",
  "this",
  "these",
  "those",
  "it",
  "its",
  "and",
  "or",
  "but",
  "if",
  "then",
  "else",
  "when",
  "at",
  "by",
  "for",
  "with",
  "about",
  "to",
  "from",
  "in",
  "on",
  "of",
  "not",
  "no",
  "so",
  "up",
  "out",
  "just",
  "also",
  "than",
  "too",
  "very",
  "what",
  "how",
  "why",
  "who",
  "which",
  "where",
  "there",
  "here",
  "all",
  "each",
  "every",
  "both",
  "few",
  "more",
  "most",
  "some",
  "any",
  "such",
  "only",
  "own",
  "same",
  "as",
  "into",
  "through",
  "during",
  "before",
  "after",
  "above",
  "below",
  "between",
  "under",
  "again",
  "further",
  "once",
  "please",
  "thanks",
  "you",
  "your",
  "we",
  "our",
  "they",
  "them",
  "me",
  "my",
  "he",
  "she",
]);

/**
 * Extract keywords from text for RLM-style retrieval.
 * Filters stop words, keeps words >= 3 chars.
 */
export function extractKeywords(text: string): Set<string> {
  const words = text.toLowerCase().match(/\b[a-z]{3,}\b/g) ?? [];
  return new Set(words.filter((w) => !STOP_WORDS.has(w)));
}
