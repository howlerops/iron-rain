import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';

export class OllamaBridge implements CLIBridge {
  readonly name = 'ollama';
  private apiBase: string;
  private model: string;

  constructor(opts: { apiBase?: string; model: string }) {
    this.apiBase = opts.apiBase ?? 'http://localhost:11434';
    this.model = opts.model;
  }

  async available(): Promise<boolean> {
    try {
      const res = await fetch(`${this.apiBase}/api/tags`, {
        signal: AbortSignal.timeout(3000),
      });
      return res.ok;
    } catch {
      return false;
    }
  }

  async execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult> {
    const start = Date.now();
    const res = await fetch(`${this.apiBase}/api/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: 'system' as const, content: options.systemPrompt }]
            : []),
          { role: 'user' as const, content: prompt },
        ],
        stream: false,
        options: {
          temperature: options?.temperature ?? 0.7,
          num_predict: options?.maxTokens ?? 4096,
        },
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      const text = await res.text();
      throw new Error(`Ollama API error (${res.status}): ${text}`);
    }

    const data = (await res.json()) as {
      message: { content: string };
      eval_count?: number;
      prompt_eval_count?: number;
    };

    return {
      content: data.message.content,
      tokens: {
        input: data.prompt_eval_count ?? 0,
        output: data.eval_count ?? 0,
      },
      model: this.model,
      duration: Date.now() - start,
    };
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    const res = await fetch(`${this.apiBase}/api/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: 'system' as const, content: options.systemPrompt }]
            : []),
          { role: 'user' as const, content: prompt },
        ],
        stream: true,
        options: {
          temperature: options?.temperature ?? 0.7,
          num_predict: options?.maxTokens ?? 4096,
        },
      }),
      signal: options?.signal,
    });

    if (!res.ok) {
      yield { type: 'error', content: `Ollama API error: ${res.status}` };
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
        if (!line.trim()) continue;
        try {
          const parsed = JSON.parse(line) as {
            message: { content: string };
            done: boolean;
          };
          if (parsed.message?.content) {
            yield { type: 'text', content: parsed.message.content };
          }
          if (parsed.done) {
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
