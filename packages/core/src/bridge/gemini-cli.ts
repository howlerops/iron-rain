import { spawn } from 'node:child_process';
import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';

export class GeminiCLIBridge implements CLIBridge {
  readonly name = 'gemini-cli';
  private model: string;
  private binaryPath: string;

  constructor(opts: { model?: string; binaryPath?: string }) {
    this.model = opts.model ?? 'gemini-2.5-pro';
    this.binaryPath = opts.binaryPath ?? 'gemini';
  }

  async available(): Promise<boolean> {
    return new Promise((resolve) => {
      const proc = spawn(this.binaryPath, ['--version'], {
        stdio: ['ignore', 'pipe', 'pipe'],
        timeout: 5000,
      });
      proc.on('close', (code) => resolve(code === 0));
      proc.on('error', () => resolve(false));
    });
  }

  async execute(prompt: string, options?: BridgeOptions): Promise<BridgeResult> {
    const start = Date.now();
    const args = [
      '-p', prompt,
      '--output-format', 'json',
      '--model', this.model,
    ];

    return new Promise((resolve, reject) => {
      const proc = spawn(this.binaryPath, args, {
        stdio: ['ignore', 'pipe', 'pipe'],
        signal: options?.signal,
      });

      let stdout = '';
      let stderr = '';

      proc.stdout.on('data', (chunk: Buffer) => { stdout += chunk.toString(); });
      proc.stderr.on('data', (chunk: Buffer) => { stderr += chunk.toString(); });

      proc.on('close', (code) => {
        const duration = Date.now() - start;

        if (code !== 0) {
          reject(new Error(`Gemini CLI exited with code ${code}: ${stderr}`));
          return;
        }

        try {
          const data = JSON.parse(stdout) as {
            result?: string;
            response?: string;
          };

          resolve({
            content: data.result ?? data.response ?? stdout.trim(),
            tokens: { input: 0, output: 0 },
            model: this.model,
            duration,
          });
        } catch {
          resolve({
            content: stdout.trim(),
            tokens: { input: 0, output: 0 },
            model: this.model,
            duration,
          });
        }
      });

      proc.on('error', (err) => {
        reject(new Error(`Failed to spawn gemini: ${err.message}`));
      });
    });
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    const args = [
      '-p', prompt,
      '--output-format', 'stream-json',
      '--model', this.model,
    ];

    const proc = spawn(this.binaryPath, args, {
      stdio: ['ignore', 'pipe', 'pipe'],
      signal: options?.signal,
    });

    let buffer = '';

    for await (const chunk of proc.stdout) {
      buffer += chunk.toString();
      const lines = buffer.split('\n');
      buffer = lines.pop() ?? '';

      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const parsed = JSON.parse(line) as {
            type: string;
            content?: string;
            text?: string;
          };

          if (parsed.content || parsed.text) {
            yield { type: 'text', content: parsed.content ?? parsed.text ?? '' };
          }
        } catch {
          // Raw text output
          if (line.trim()) {
            yield { type: 'text', content: line };
          }
        }
      }
    }

    yield { type: 'done', content: '' };
  }
}
