import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';
import { resolveEnvValue } from '../config/schema.js';

export class OpenAICompatBridge implements CLIBridge {
  readonly name: string;
  private apiBase: string;
  private apiKey: string;
  private model: string;

  constructor(opts: { name?: string; apiBase: string; apiKey: string; model: string }) {
    this.name = opts.name ?? 'openai-compat';
    this.apiBase = opts.apiBase;
    this.apiKey = resolveEnvValue(opts.apiKey);
    this.model = opts.model;
  }

  async available(): Promise<boolean> {
    try {
      const res = await fetch(`${this.apiBase}/models`, {
        headers: { Authorization: `Bearer ${this.apiKey}` },
        signal: AbortSignal.timeout(5000),
      });
      return res.ok;
    } catch {
      return false;
    }
  }

  async execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult> {
    const start = Date.now();
    const res = await fetch(`${this.apiBase}/chat/completions`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: 'system' as const, content: options.systemPrompt }]
            : []),
          { role: 'user' as const, content: prompt },
        ],
        max_tokens: options?.maxTokens ?? 4096,
        temperature: options?.temperature ?? 0.7,
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      const text = await res.text();
      throw new Error(`OpenAI-compat API error (${res.status}): ${text}`);
    }

    const data = (await res.json()) as {
      choices: Array<{ message: { content: string } }>;
      usage?: { prompt_tokens: number; completion_tokens: number };
    };

    return {
      content: data.choices[0]?.message?.content ?? '',
      tokens: {
        input: data.usage?.prompt_tokens ?? 0,
        output: data.usage?.completion_tokens ?? 0,
      },
      model: this.model,
      duration: Date.now() - start,
    };
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    const res = await fetch(`${this.apiBase}/chat/completions`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: 'system' as const, content: options.systemPrompt }]
            : []),
          { role: 'user' as const, content: prompt },
        ],
        max_tokens: options?.maxTokens ?? 4096,
        temperature: options?.temperature ?? 0.7,
        stream: true,
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      yield { type: 'error', content: `API error: ${res.status}` };
      return;
    }

    const reader = res.body?.getReader();
    if (!reader) {
      yield { type: 'error', content: 'No response body' };
      return;
    }

    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() ?? '';

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue;
        const data = line.slice(6);
        if (data === '[DONE]') {
          yield { type: 'done', content: '' };
          return;
        }
        try {
          const parsed = JSON.parse(data) as {
            choices: Array<{ delta: { content?: string } }>;
          };
          const content = parsed.choices[0]?.delta?.content;
          if (content) {
            yield { type: 'text', content };
          }
        } catch {
          // skip malformed chunks
        }
      }
    }

    yield { type: 'done', content: '' };
  }
}
