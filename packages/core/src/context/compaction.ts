/**
 * Context Compaction with RLM (Retrieval-augmented Language Model) pattern.
 *
 * Strategy:
 * - Keep last N messages verbatim (the "hot window")
 * - Compact older messages into summaries (the "cold archive")
 * - Use keyword retrieval to pull relevant archived messages back
 *   into context when the current prompt matches them
 *
 * This lets conversations go much longer without overwhelming context.
 */

export interface CompactedMessage {
  role: "user" | "assistant";
  content: string;
  /** Original message ID for deduplication */
  originalId?: string;
  /** Whether this is a compacted summary vs original */
  isCompacted?: boolean;
}

export interface ContextWindow {
  /** System prompt pieces */
  systemParts: string[];
  /** Messages to send to the model */
  messages: CompactedMessage[];
  /** Total estimated tokens */
  estimatedTokens: number;
  /** Number of messages that were compacted */
  compactedCount: number;
}

export interface CompactionConfig {
  /** Messages to keep verbatim (the hot window). Default: 6 */
  hotWindowSize: number;
  /** Max estimated tokens for the full context. Default: 8000 */
  maxContextTokens: number;
  /** Number of archived messages to retrieve via RLM. Default: 3 */
  rlmRetrievalCount: number;
  /** Trigger compaction when message count exceeds this. Default: 8 */
  compactionThreshold: number;
}

export const DEFAULT_COMPACTION_CONFIG: CompactionConfig = {
  hotWindowSize: 6,
  maxContextTokens: 8000,
  rlmRetrievalCount: 3,
  compactionThreshold: 8,
};

/** Rough token estimate: ~4 chars per token */
function estimateTokens(text: string): number {
  return Math.ceil(text.length / 4);
}

/**
 * Extract keywords from text for RLM retrieval.
 * Simple but effective: split on whitespace, filter short/common words.
 */
function extractKeywords(text: string): Set<string> {
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
    "i",
    "me",
    "my",
    "we",
    "our",
    "you",
    "your",
    "he",
    "she",
    "they",
    "them",
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
  ]);

  const words = text.toLowerCase().match(/\b[a-z]{3,}\b/g) ?? [];
  return new Set(words.filter((w) => !STOP_WORDS.has(w)));
}

/**
 * Score how relevant an archived message is to the current query.
 * Uses keyword overlap (Jaccard-like scoring).
 */
function relevanceScore(
  queryKeywords: Set<string>,
  messageKeywords: Set<string>,
): number {
  if (queryKeywords.size === 0 || messageKeywords.size === 0) return 0;
  let overlap = 0;
  for (const kw of queryKeywords) {
    if (messageKeywords.has(kw)) overlap++;
  }
  return overlap / queryKeywords.size;
}

/**
 * Compact a sequence of messages into a brief summary.
 * Uses a simple extractive approach — no LLM call needed.
 */
function compactMessages(messages: CompactedMessage[]): string {
  if (messages.length === 0) return "";

  const parts: string[] = [];
  for (const msg of messages) {
    const preview =
      msg.content.length > 120
        ? `${msg.content.slice(0, 117)}...`
        : msg.content;
    parts.push(`[${msg.role}] ${preview}`);
  }

  return `[Summary of ${messages.length} earlier messages]\n${parts.join("\n")}`;
}

/**
 * Build an optimized context window using RLM pattern.
 *
 * 1. Keep last `hotWindowSize` messages verbatim
 * 2. Archive older messages
 * 3. Use keyword retrieval to find relevant archived messages
 * 4. Compact remaining archived messages into a summary
 */
export function buildContextWindow(
  allMessages: CompactedMessage[],
  currentPrompt: string,
  config: CompactionConfig = DEFAULT_COMPACTION_CONFIG,
): ContextWindow {
  // If under threshold, just return everything
  if (allMessages.length <= config.compactionThreshold) {
    return {
      systemParts: [],
      messages: allMessages,
      estimatedTokens: allMessages.reduce(
        (sum, m) => sum + estimateTokens(m.content),
        0,
      ),
      compactedCount: 0,
    };
  }

  // Split into hot (recent) and cold (archive)
  const hotStart = Math.max(0, allMessages.length - config.hotWindowSize);
  const hotMessages = allMessages.slice(hotStart);
  const coldMessages = allMessages.slice(0, hotStart);

  // RLM retrieval: find relevant archived messages
  const queryKeywords = extractKeywords(currentPrompt);
  const coldWithScores = coldMessages.map((msg, idx) => ({
    msg,
    idx,
    score: relevanceScore(queryKeywords, extractKeywords(msg.content)),
  }));

  // Sort by relevance, take top N
  coldWithScores.sort((a, b) => b.score - a.score);
  const retrievedIndices = new Set(
    coldWithScores
      .slice(0, config.rlmRetrievalCount)
      .filter((s) => s.score > 0)
      .map((s) => s.idx),
  );

  // Build the context: summary + retrieved + hot
  const systemParts: string[] = [];
  const unretrievedCold = coldMessages.filter(
    (_, i) => !retrievedIndices.has(i),
  );

  if (unretrievedCold.length > 0) {
    systemParts.push(compactMessages(unretrievedCold));
  }

  const retrievedMessages: CompactedMessage[] = coldMessages
    .filter((_, i) => retrievedIndices.has(i))
    .map((m) => ({ ...m, isCompacted: false }));

  const finalMessages = [...retrievedMessages, ...hotMessages];

  const totalTokens =
    systemParts.reduce((sum, p) => sum + estimateTokens(p), 0) +
    finalMessages.reduce((sum, m) => sum + estimateTokens(m.content), 0);

  return {
    systemParts,
    messages: finalMessages,
    estimatedTokens: totalTokens,
    compactedCount: unretrievedCold.length,
  };
}
