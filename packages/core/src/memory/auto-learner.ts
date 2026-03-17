import type { OrchestratorKernel } from "../orchestrator/kernel.js";
import type { ChatMessage } from "../bridge/types.js";
import { getTextContent } from "../bridge/types.js";

export interface Lesson {
  id: string;
  content: string;
  keywords: string[];
  createdAt: number;
}

const EXTRACT_PROMPT = `Analyze this conversation and extract 1-3 key lessons learned. Each lesson should be a concise, actionable insight that would be useful in future sessions.

Focus on:
- Project conventions or patterns discovered
- User preferences for workflow or tools
- Solutions to problems that were hard to find
- Important file paths or architecture decisions

Return JSON array: [{"content": "lesson text", "keywords": ["keyword1", "keyword2"]}]
Return empty array [] if no significant lessons.`;

/**
 * Automatically extracts lessons from session conversations.
 */
export class AutoLearner {
  private lastLearnTime = 0;
  private readonly minIntervalMs = 600_000; // 10 minutes
  private readonly minMessages = 5;

  /**
   * Check if enough time and messages have passed to trigger learning.
   */
  shouldLearn(messageCount: number): boolean {
    if (messageCount < this.minMessages) return false;
    if (Date.now() - this.lastLearnTime < this.minIntervalMs) return false;
    return true;
  }

  /**
   * Extract lessons from conversation history via the main slot.
   */
  async extractLessons(
    messages: ChatMessage[],
    kernel: OrchestratorKernel,
  ): Promise<Lesson[]> {
    if (messages.length < this.minMessages) return [];

    this.lastLearnTime = Date.now();

    // Build conversation summary (last 20 messages max)
    const recent = messages.slice(-20);
    const summary = recent
      .map((m) => `${m.role}: ${getTextContent(m.content).slice(0, 200)}`)
      .join("\n");

    try {
      const episode = await kernel.dispatch({
        id: `learn-${Date.now()}`,
        prompt: `${EXTRACT_PROMPT}\n\n## Conversation:\n${summary}`,
        targetSlot: "main",
      });

      return this.parseLessons(episode.result ?? "");
    } catch {
      return [];
    }
  }

  private parseLessons(response: string): Lesson[] {
    try {
      // Extract JSON from response (may be wrapped in markdown code blocks)
      const jsonMatch = response.match(/\[[\s\S]*\]/);
      if (!jsonMatch) return [];

      const parsed = JSON.parse(jsonMatch[0]) as Array<{
        content: string;
        keywords: string[];
      }>;

      return parsed
        .filter((l) => l.content && l.keywords)
        .map((l) => ({
          id: `lesson-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
          content: l.content,
          keywords: l.keywords,
          createdAt: Date.now(),
        }));
    } catch {
      return [];
    }
  }

  /**
   * Find lessons relevant to a given prompt by keyword matching.
   */
  filterRelevant(lessons: Lesson[], prompt: string, maxCount = 10): Lesson[] {
    const promptLower = prompt.toLowerCase();
    const promptWords = new Set(promptLower.split(/\W+/).filter((w) => w.length > 3));

    const scored = lessons.map((lesson) => {
      let score = 0;
      for (const kw of lesson.keywords) {
        if (promptLower.includes(kw.toLowerCase())) score += 2;
        if (promptWords.has(kw.toLowerCase())) score += 1;
      }
      return { lesson, score };
    });

    return scored
      .filter((s) => s.score > 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, maxCount)
      .map((s) => s.lesson);
  }
}
