import { resolveEnvValue } from "../config/schema.js";
import { BaseAPIBridge } from "./base-api.js";
import { geminiThinkingBudget } from "./thinking.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";
import { getTextContent } from "./types.js";

export class GeminiBridge extends BaseAPIBridge {
  constructor(opts: { apiKey: string; model: string }) {
    super({
      name: "gemini",
      apiKey: resolveEnvValue(opts.apiKey),
      model: opts.model,
    });
  }

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const url = `https://generativelanguage.googleapis.com/v1beta/models/${this.model}:generateContent?key=${this.apiKey}`;

    const contents = this.buildContents(prompt, options);
    const body = this.buildRequestBody(contents, options);

    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: options?.signal,
    });

    await this.assertOk(res);

    const data = (await res.json()) as {
      candidates: Array<{
        content: { parts: Array<{ text: string }> };
      }>;
      usageMetadata?: {
        promptTokenCount: number;
        candidatesTokenCount: number;
      };
    };

    const text =
      data.candidates?.[0]?.content?.parts?.map((p) => p.text).join("") ?? "";

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

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    const url = `https://generativelanguage.googleapis.com/v1beta/models/${this.model}:streamGenerateContent?key=${this.apiKey}&alt=sse`;

    const contents = this.buildContents(prompt, options);
    const body = this.buildRequestBody(contents, options);

    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: options?.signal,
    });

    await this.assertOk(res);

    this.resetTokenCounts();

    try {
      for await (const line of this.streamSSE(res)) {
        if (!line.startsWith("data: ")) continue;
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
            this.setInputTokens(parsed.usageMetadata.promptTokenCount);
            this.setOutputTokens(parsed.usageMetadata.candidatesTokenCount);
          }
          const text = parsed.candidates?.[0]?.content?.parts?.[0]?.text;
          if (text) {
            yield { type: "text", content: text };
          }
        } catch {
          // skip malformed
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

  private buildContents(
    prompt: string,
    options?: BridgeOptions,
  ): Array<{ role: string; parts: Array<{ text: string }> }> {
    const contents: Array<{ role: string; parts: Array<{ text: string }> }> = [];
    if (options?.conversationHistory) {
      for (const m of options.conversationHistory) {
        contents.push({
          role: m.role === "assistant" ? "model" : "user",
          parts: [{ text: getTextContent(m.content) }],
        });
      }
    }
    contents.push({ role: "user", parts: [{ text: prompt }] });
    return contents;
  }

  private buildRequestBody(
    contents: Array<{ role: string; parts: Array<{ text: string }> }>,
    options?: BridgeOptions,
  ): Record<string, unknown> {
    return {
      contents,
      ...(options?.systemPrompt
        ? { system_instruction: { parts: [{ text: options.systemPrompt }] } }
        : {}),
      generationConfig: {
        maxOutputTokens: options?.maxTokens ?? 4096,
        temperature: options?.temperature ?? 0.7,
        ...(options?.thinkingLevel
          ? (() => {
              const budget = geminiThinkingBudget(options.thinkingLevel!);
              return budget != null
                ? { thinkingConfig: { thinkingBudget: budget } }
                : {};
            })()
          : {}),
      },
    };
  }
}
