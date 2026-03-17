import { resolveEnvValue } from "../config/schema.js";
import { anthropicThinkingBudget } from "./thinking.js";
import { BaseAPIBridge } from "./base-api.js";
import type { BridgeChunk, BridgeOptions, BridgeResult } from "./types.js";

export class AnthropicBridge extends BaseAPIBridge {
  constructor(opts: { apiKey: string; model: string }) {
    super({
      name: "anthropic",
      apiKey: resolveEnvValue(opts.apiKey),
      model: opts.model,
    });
  }

  async execute(
    prompt: string,
    options?: BridgeOptions,
  ): Promise<BridgeResult> {
    const start = Date.now();
    const budget = options?.thinkingLevel
      ? anthropicThinkingBudget(options.thinkingLevel)
      : null;
    const maxTokens = budget
      ? Math.max((options?.maxTokens ?? 4096) + budget, budget + 1024)
      : (options?.maxTokens ?? 4096);
    const res = await fetch("https://api.anthropic.com/v1/messages", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": this.apiKey,
        "anthropic-version": "2023-06-01",
      },
      body: JSON.stringify({
        model: this.model,
        max_tokens: maxTokens,
        ...(options?.systemPrompt ? { system: options.systemPrompt } : {}),
        ...(budget
          ? { thinking: { type: "enabled", budget_tokens: budget } }
          : {}),
        messages: [
          ...(options?.conversationHistory?.map((m) => ({
            role: m.role as "user" | "assistant",
            content: m.content,
          })) ?? []),
          { role: "user", content: prompt },
        ],
      }),
      signal: options?.signal,
    });

    await this.assertOk(res);

    const data = (await res.json()) as {
      content: Array<{ type: string; text: string }>;
      usage: { input_tokens: number; output_tokens: number };
    };

    const text = data.content
      .filter((b) => b.type === "text")
      .map((b) => b.text)
      .join("");

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

  async *stream(
    prompt: string,
    options?: BridgeOptions,
  ): AsyncIterable<BridgeChunk> {
    const budget = options?.thinkingLevel
      ? anthropicThinkingBudget(options.thinkingLevel)
      : null;
    const maxTokens = budget
      ? Math.max((options?.maxTokens ?? 4096) + budget, budget + 1024)
      : (options?.maxTokens ?? 4096);
    const res = await fetch("https://api.anthropic.com/v1/messages", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": this.apiKey,
        "anthropic-version": "2023-06-01",
      },
      body: JSON.stringify({
        model: this.model,
        max_tokens: maxTokens,
        ...(options?.systemPrompt ? { system: options.systemPrompt } : {}),
        ...(budget
          ? { thinking: { type: "enabled", budget_tokens: budget } }
          : {}),
        messages: [
          ...(options?.conversationHistory?.map((m) => ({
            role: m.role as "user" | "assistant",
            content: m.content,
          })) ?? []),
          { role: "user", content: prompt },
        ],
        stream: true,
      }),
      signal: options?.signal,
    });

    await this.assertOk(res);

    this.resetTokenCounts();
    const activeToolBlocks = new Map<number, { id: string; name: string }>();
    const toolInputBuffers = new Map<number, string>();

    try {
      for await (const line of this.streamSSE(res)) {
        if (!line.startsWith("data: ")) continue;
        try {
          const parsed = JSON.parse(line.slice(6)) as {
            type: string;
            content_block?: { type: string; id?: string; name?: string };
            index?: number;
            message?: { usage?: { input_tokens?: number } };
            delta?: { type: string; text?: string; thinking?: string; partial_json?: string };
            usage?: { output_tokens?: number };
          };
          if (
            parsed.type === "message_start" &&
            parsed.message?.usage?.input_tokens
          ) {
            this.inputTokens = parsed.message.usage.input_tokens;
          }
          if (
            parsed.type === "content_block_start" &&
            parsed.content_block?.type === "tool_use" &&
            parsed.content_block.id &&
            parsed.content_block.name &&
            parsed.index != null
          ) {
            const { id, name } = parsed.content_block;
            activeToolBlocks.set(parsed.index, { id, name });
            toolInputBuffers.set(parsed.index, "");
            yield {
              type: "tool_use" as const,
              content: name,
              toolCall: { id, name, status: "start" as const },
            };
          }
          if (parsed.type === "content_block_stop" && parsed.index != null) {
            const tool = activeToolBlocks.get(parsed.index);
            if (tool) {
              // Try to enrich tool name from accumulated input JSON
              const inputBuf = toolInputBuffers.get(parsed.index) ?? "";
              let enrichedName = tool.name;
              if (inputBuf) {
                try {
                  const input = JSON.parse(inputBuf) as Record<string, any>;
                  const filePath = input.file_path ?? input.path;
                  if (filePath && typeof filePath === "string") {
                    enrichedName = `${tool.name} ${filePath.split("/").pop()}`;
                  } else if (input.pattern && typeof input.pattern === "string") {
                    const p = input.pattern.length > 30 ? input.pattern.slice(0, 27) + "..." : input.pattern;
                    enrichedName = `${tool.name} ${p}`;
                  } else if (input.command && typeof input.command === "string") {
                    const c = input.command.length > 40 ? input.command.slice(0, 37) + "..." : input.command;
                    enrichedName = `${tool.name}: ${c}`;
                  }
                } catch { /* partial JSON, keep original name */ }
              }
              activeToolBlocks.delete(parsed.index);
              toolInputBuffers.delete(parsed.index);
              yield {
                type: "tool_use" as const,
                content: enrichedName,
                toolCall: { id: tool.id, name: enrichedName, status: "end" as const },
              };
            }
          }
          if (parsed.type === "content_block_delta") {
            if (parsed.delta?.type === "input_json_delta" && parsed.delta?.partial_json && parsed.index != null) {
              const buf = toolInputBuffers.get(parsed.index);
              if (buf != null) {
                toolInputBuffers.set(parsed.index, buf + parsed.delta.partial_json);
              }
            } else if (parsed.delta?.type === "thinking_delta" && parsed.delta?.thinking) {
              yield { type: "thinking", content: parsed.delta.thinking };
            } else if (parsed.delta?.text) {
              yield { type: "text", content: parsed.delta.text };
            }
          }
          if (parsed.type === "message_delta" && parsed.usage?.output_tokens) {
            this.outputTokens = parsed.usage.output_tokens;
          }
          if (parsed.type === "message_stop") {
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
    } catch (err) {
      yield {
        type: "error",
        content: err instanceof Error ? err.message : "No response body",
      };
      return;
    }

    yield {
      type: "done",
      content: "",
      tokens: this.getTokenCounts(),
    };
  }
}
