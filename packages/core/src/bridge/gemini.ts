import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';
import { getTextContent } from './types.js';
import { resolveEnvValue } from '../config/schema.js';
import { geminiThinkingBudget } from './thinking.js';

export class GeminiBridge implements CLIBridge {
  readonly name = 'gemini';
  private apiKey: string;
  private model: string;

  constructor(opts: { apiKey: string; model: string }) {
    this.apiKey = resolveEnvValue(opts.apiKey);
    this.model = opts.model;
  }

  async available(): Promise<boolean> {
    return this.apiKey.length > 0;
  }

  async execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult> {
    const start = Date.now();
    const url = `https://generativelanguage.googleapis.com/v1beta/models/${this.model}:generateContent?key=${this.apiKey}`;

    const contents = [];
    if (options?.systemPrompt) {
      contents.push({ role: 'user', parts: [{ text: options.systemPrompt }] });
      contents.push({ role: 'model', parts: [{ text: 'Understood.' }] });
    }
    if (options?.conversationHistory) {
      for (const m of options.conversationHistory) {
        contents.push({ role: m.role === 'assistant' ? 'model' : 'user', parts: [{ text: getTextContent(m.content) }] });
      }
    }
    contents.push({ role: 'user', parts: [{ text: prompt }] });

    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        contents,
        generationConfig: {
          maxOutputTokens: options?.maxTokens ?? 4096,
          temperature: options?.temperature ?? 0.7,
          ...(options?.thinkingLevel ? (() => {
            const budget = geminiThinkingBudget(options.thinkingLevel!);
            return budget != null ? { thinkingConfig: { thinkingBudget: budget } } : {};
          })() : {}),
        },
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      const text = await res.text();
      throw new Error(`Gemini API error (${res.status}): ${text}`);
    }

    const data = (await res.json()) as {
      candidates: Array<{
        content: { parts: Array<{ text: string }> };
      }>;
      usageMetadata?: {
        promptTokenCount: number;
        candidatesTokenCount: number;
      };
    };

    const text = data.candidates?.[0]?.content?.parts
      ?.map((p) => p.text)
      .join('') ?? '';

    return {
      content: text,
      tokens: {
        input: data.usageMetadata?.promptTokenCount ?? 0,
        output: data.usageMetadata?.candidatesTokenCount ?? 0,
      },
      model: this.model,
      duration: Date.now() - start,
    };
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    const url = `https://generativelanguage.googleapis.com/v1beta/models/${this.model}:streamGenerateContent?key=${this.apiKey}&alt=sse`;

    const contents = [];
    if (options?.systemPrompt) {
      contents.push({ role: 'user', parts: [{ text: options.systemPrompt }] });
      contents.push({ role: 'model', parts: [{ text: 'Understood.' }] });
    }
    if (options?.conversationHistory) {
      for (const m of options.conversationHistory) {
        contents.push({ role: m.role === 'assistant' ? 'model' : 'user', parts: [{ text: getTextContent(m.content) }] });
      }
    }
    contents.push({ role: 'user', parts: [{ text: prompt }] });

    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        contents,
        generationConfig: {
          maxOutputTokens: options?.maxTokens ?? 4096,
          temperature: options?.temperature ?? 0.7,
          ...(options?.thinkingLevel ? (() => {
            const budget = geminiThinkingBudget(options.thinkingLevel!);
            return budget != null ? { thinkingConfig: { thinkingBudget: budget } } : {};
          })() : {}),
        },
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      yield { type: 'error', content: `Gemini API error: ${res.status}` };
      return;
    }

    const reader = res.body?.getReader();
    if (!reader) {
      yield { type: 'error', content: 'No response body' };
      return;
    }

    const decoder = new TextDecoder();
    let buffer = '';
    let inputTokens = 0;
    let outputTokens = 0;

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() ?? '';

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue;
        try {
          const parsed = JSON.parse(line.slice(6)) as {
            candidates: Array<{
              content: { parts: Array<{ text: string }> };
            }>;
            usageMetadata?: {
              promptTokenCount: number;
              candidatesTokenCount: number;
            };
          };
          if (parsed.usageMetadata) {
            inputTokens = parsed.usageMetadata.promptTokenCount;
            outputTokens = parsed.usageMetadata.candidatesTokenCount;
          }
          const text = parsed.candidates?.[0]?.content?.parts?.[0]?.text;
          if (text) {
            yield { type: 'text', content: text };
          }
        } catch {
          // skip malformed
        }
      }
    }

    yield { type: 'done', content: '', tokens: { input: inputTokens, output: outputTokens } };
  }
}
