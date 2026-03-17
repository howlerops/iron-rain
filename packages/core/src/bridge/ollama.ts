import { BaseAPIBridge } from "./base-api.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";
import { getTextContent } from "./types.js";

export class OllamaBridge extends BaseAPIBridge {
  constructor(opts: { apiBase?: string; model: string }) {
    super({
      name: "ollama",
      apiBase: opts.apiBase ?? "http://localhost:11434",
      apiKey: "",
      model: opts.model,
    });
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

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const res = await fetch(`${this.apiBase}/api/chat`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: "system" as const, content: options.systemPrompt }]
            : []),
          ...(options?.conversationHistory?.map((m) => ({
            role: m.role as string,
            content: getTextContent(m.content),
          })) ?? []),
          { role: "user" as const, content: prompt },
        ],
        stream: false,
        options: {
          temperature: options?.temperature ?? 0.7,
          num_predict: options?.maxTokens ?? 4096,
        },
      }),
      signal: options?.signal,
    });

    await this.assertOk(res);

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

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    const res = await fetch(`${this.apiBase}/api/chat`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: "system" as const, content: options.systemPrompt }]
            : []),
          ...(options?.conversationHistory?.map((m) => ({
            role: m.role as string,
            content: getTextContent(m.content),
          })) ?? []),
          { role: "user" as const, content: prompt },
        ],
        stream: true,
        options: {
          temperature: options?.temperature ?? 0.7,
          num_predict: options?.maxTokens ?? 4096,
        },
      }),
      signal: options?.signal,
    });

    await this.assertOk(res);

    this.resetTokenCounts();

    try {
      for await (const line of this.streamSSE(res)) {
        if (!line.trim()) continue;
        try {
          const parsed = JSON.parse(line) as {
            message: { content: string };
            done: boolean;
            prompt_eval_count?: number;
            eval_count?: number;
          };
          if (parsed.message?.content) {
            yield { type: "text", content: parsed.message.content };
          }
          if (parsed.done) {
            this.setInputTokens(parsed.prompt_eval_count ?? 0);
            this.setOutputTokens(parsed.eval_count ?? 0);
            yield {
              type: "done",
              content: "",
              tokens: this.getTokenCounts(),
            };
            return;
          }
        } catch {
          // skip malformed
        }
      }
    } catch {
      yield { type: "error", content: "No response body" };
      return;
    }

    yield { type: "done", content: "" };
  }
}
