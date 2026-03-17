import { resolveEnvValue } from "../config/schema.js";
import { BaseAPIBridge } from "./base-api.js";
import { openaiReasoningEffort } from "./thinking.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";
import { getTextContent } from "./types.js";

export class OpenAICompatBridge extends BaseAPIBridge {
  constructor(opts: {
    name?: string;
    apiBase: string;
    apiKey: string;
    model: string;
  }) {
    super({
      name: opts.name ?? "openai-compat",
      apiBase: opts.apiBase,
      apiKey: resolveEnvValue(opts.apiKey),
      model: opts.model,
    });
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

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const res = await fetch(`${this.apiBase}/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: "system" as const, content: options.systemPrompt }]
            : []),
          ...(options?.conversationHistory?.map((m) => ({
            role: m.role as "user" | "assistant" | "system",
            content: getTextContent(m.content),
          })) ?? []),
          { role: "user" as const, content: prompt },
        ],
        max_tokens: options?.maxTokens ?? 4096,
        temperature: options?.temperature ?? 0.7,
        ...(options?.thinkingLevel
          ? (() => {
              const effort = openaiReasoningEffort(options.thinkingLevel!);
              return effort ? { reasoning_effort: effort } : {};
            })()
          : {}),
      }),
      signal: options?.signal,
    });

    await this.assertOk(res);

    const data = (await res.json()) as {
      choices: Array<{ message: { content: string } }>;
      usage?: { prompt_tokens: number; completion_tokens: number };
    };

    return {
      content: data.choices[0]?.message?.content ?? "",
      tokens: {
        input: data.usage?.prompt_tokens ?? 0,
        output: data.usage?.completion_tokens ?? 0,
      },
      model: this.model,
      duration: Date.now() - start,
    };
  }

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    const res = await fetch(`${this.apiBase}/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify({
        model: this.model,
        messages: [
          ...(options?.systemPrompt
            ? [{ role: "system" as const, content: options.systemPrompt }]
            : []),
          ...(options?.conversationHistory?.map((m) => ({
            role: m.role as "user" | "assistant" | "system",
            content: getTextContent(m.content),
          })) ?? []),
          { role: "user" as const, content: prompt },
        ],
        max_tokens: options?.maxTokens ?? 4096,
        temperature: options?.temperature ?? 0.7,
        stream: true,
        stream_options: { include_usage: true },
        ...(options?.thinkingLevel
          ? (() => {
              const effort = openaiReasoningEffort(options.thinkingLevel!);
              return effort ? { reasoning_effort: effort } : {};
            })()
          : {}),
      }),
      signal: options?.signal,
    });

    await this.assertOk(res);

    this.resetTokenCounts();

    try {
      for await (const line of this.streamSSE(res)) {
        if (!line.startsWith("data: ")) continue;
        const data = line.slice(6);
        if (data === "[DONE]") {
          yield {
            type: "done",
            content: "",
            tokens: this.getTokenCounts(),
          };
          return;
        }
        try {
          const parsed = JSON.parse(data) as {
            choices: Array<{ delta: { content?: string } }>;
            usage?: { prompt_tokens: number; completion_tokens: number };
          };
          if (parsed.usage) {
            this.setInputTokens(parsed.usage.prompt_tokens);
            this.setOutputTokens(parsed.usage.completion_tokens);
          }
          const content = parsed.choices?.[0]?.delta?.content;
          if (content) {
            yield { type: "text", content };
          }
        } catch {
          // skip malformed chunks
        }
      }
    } catch {
      yield { type: "error", content: "No response body" };
      return;
    }

    yield {
      type: "done",
      content: "",
      tokens: this.getTokenCounts(),
    };
  }
}
