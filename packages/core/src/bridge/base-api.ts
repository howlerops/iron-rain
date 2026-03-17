import { createBridgeError } from "./errors.js";
import type {
  BridgeChunk,
  BridgeOptions,
  BridgeResult,
  CLIBridge,
} from "./types.js";

export abstract class BaseAPIBridge implements CLIBridge {
  readonly name: string;
  protected apiKey: string;
  protected model: string;
  protected apiBase?: string;
  protected inputTokens = 0;
  protected outputTokens = 0;

  constructor(opts: {
    name: string;
    apiKey: string;
    model: string;
    apiBase?: string;
  }) {
    this.name = opts.name;
    this.apiKey = opts.apiKey;
    this.model = opts.model;
    this.apiBase = opts.apiBase;
  }

  async available(): Promise<boolean> {
    return this.apiKey.length > 0;
  }

  protected resetTokenCounts(): void {
    this.inputTokens = 0;
    this.outputTokens = 0;
  }

  protected setInputTokens(tokens: number): void {
    this.inputTokens = tokens;
  }

  protected setOutputTokens(tokens: number): void {
    this.outputTokens = tokens;
  }

  protected getTokenCounts(): { input: number; output: number } {
    return { input: this.inputTokens, output: this.outputTokens };
  }

  protected async *streamSSE(res: Response): AsyncIterable<string> {
    const reader = res.body?.getReader();
    if (!reader) {
      throw new Error("No response body");
    }

    const decoder = new TextDecoder();
    let buffer = "";

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";

      for (const line of lines) {
        yield line;
      }
    }

    if (buffer.length > 0) {
      yield buffer;
    }
  }

  /**
   * Check response status and throw BridgeError on non-ok responses.
   */
  protected async assertOk(res: Response): Promise<void> {
    if (!res.ok) {
      const body = await res.text();
      throw createBridgeError(this.name, res.status, body);
    }
  }

  abstract execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult>;
  abstract stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk>;
}
