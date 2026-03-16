import { spawn } from 'node:child_process';
import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';

export class CodexBridge implements CLIBridge {
  readonly name = 'codex';
  private model: string;
  private binaryPath: string;

  constructor(opts: { model?: string; binaryPath?: string }) {
    this.model = opts.model ?? 'o3';
    this.binaryPath = opts.binaryPath ?? 'codex';
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
      'exec',
      prompt,
      '-c', `model="${this.model}"`,
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
          reject(new Error(`Codex exited with code ${code}: ${stderr}`));
          return;
        }

        resolve({
          content: stdout.trim(),
          tokens: { input: 0, output: 0 },
          model: this.model,
          duration,
        });
      });

      proc.on('error', (err) => {
        reject(new Error(`Failed to spawn codex: ${err.message}`));
      });
    });
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    // Codex exec doesn't support streaming — execute and yield all at once
    try {
      const result = await this.execute(prompt, options);
      yield { type: 'text', content: result.content };
    } catch (err) {
      yield { type: 'error', content: err instanceof Error ? err.message : String(err) };
    }
    yield { type: 'done', content: '' };
  }
}
