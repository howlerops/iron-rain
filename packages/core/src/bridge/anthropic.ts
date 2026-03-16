import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';
import { resolveEnvValue } from '../config/schema.js';

export class AnthropicBridge implements CLIBridge {
  readonly name = 'anthropic';
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
    const res = await fetch('https://api.anthropic.com/v1/messages', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'x-api-key': this.apiKey,
        'anthropic-version': '2023-06-01',
      },
      body: JSON.stringify({
        model: this.model,
        max_tokens: options?.maxTokens ?? 4096,
        ...(options?.systemPrompt ? { system: options.systemPrompt } : {}),
        messages: [{ role: 'user', content: prompt }],
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      const text = await res.text();
      throw new Error(`Anthropic API error (${res.status}): ${text}`);
    }

    const data = (await res.json()) as {
      content: Array<{ type: string; text: string }>;
      usage: { input_tokens: number; output_tokens: number };
    };

    const text = data.content
      .filter((b) => b.type === 'text')
      .map((b) => b.text)
      .join('');

    return {
      content: text,
      tokens: {
        input: data.usage.input_tokens,
        output: data.usage.output_tokens,
      },
      model: this.model,
      duration: Date.now() - start,
    };
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    const res = await fetch('https://api.anthropic.com/v1/messages', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'x-api-key': this.apiKey,
        'anthropic-version': '2023-06-01',
      },
      body: JSON.stringify({
        model: this.model,
        max_tokens: options?.maxTokens ?? 4096,
        ...(options?.systemPrompt ? { system: options.systemPrompt } : {}),
        messages: [{ role: 'user', content: prompt }],
        stream: true,
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      yield { type: 'error', content: `Anthropic API error: ${res.status}` };
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
        try {
          const parsed = JSON.parse(line.slice(6)) as {
            type: string;
            delta?: { type: string; text?: string };
          };
          if (parsed.type === 'content_block_delta' && parsed.delta?.text) {
            yield { type: 'text', content: parsed.delta.text };
          }
          if (parsed.type === 'message_stop') {
            yield { type: 'done', content: '' };
            return;
          }
        } catch {
          // skip malformed
        }
      }
    }

    yield { type: 'done', content: '' };
  }
}
