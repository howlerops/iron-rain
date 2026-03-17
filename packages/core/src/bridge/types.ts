import type { ThinkingLevel } from '../slots/types.js';

export interface ImageContent {
  type: 'image';
  data: string;       // base64-encoded
  mimeType: string;   // e.g. 'image/png'
}

export interface TextContent {
  type: 'text';
  text: string;
}

export type MessageContent = string | Array<TextContent | ImageContent>;

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system';
  content: MessageContent;
}

/** Extract plain text from MessageContent, joining text parts and ignoring images. */
export function getTextContent(content: MessageContent): string {
  if (typeof content === 'string') return content;
  return content
    .filter((p): p is TextContent => p.type === 'text')
    .map(p => p.text)
    .join('\n');
}

export interface BridgeOptions {
  systemPrompt?: string;
  conversationHistory?: ChatMessage[];
  maxTokens?: number;
  temperature?: number;
  signal?: AbortSignal;
  thinkingLevel?: ThinkingLevel;
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
  tokens?: { input: number; output: number };
}

export interface CLIBridge {
  name: string;
  available(): Promise<boolean>;
  execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult>;
  stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk>;
}
