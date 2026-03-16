export interface BridgeOptions {
  systemPrompt?: string;
  maxTokens?: number;
  temperature?: number;
  signal?: AbortSignal;
}

export interface BridgeResult {
  content: string;
  tokens: { input: number; output: number };
  model: string;
  duration: number;
}

export interface BridgeChunk {
  type: 'text' | 'tool_use' | 'error' | 'done';
  content: string;
}

export interface CLIBridge {
  name: string;
  available(): Promise<boolean>;
  execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult>;
  stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk>;
}
