import { spawn } from 'node:child_process';
import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk } from './types.js';

export class ClaudeCodeBridge implements CLIBridge {
  readonly name = 'claude-code';
  private model: string;
  private binaryPath: string;

  constructor(opts: { model?: string; binaryPath?: string }) {
    this.model = opts.model ?? 'sonnet';
    this.binaryPath = opts.binaryPath ?? 'claude';
  }

  async available(): Promise<boolean> {
    return new Promise((resolve) => {
      const proc = spawn(this.binaryPath, ['--version'], {
        env: { ...process.env, CLAUDECODE: '' },
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
      '--max-turns', '1',
      '--no-session-persistence',
    ];

    if (options?.systemPrompt) {
      args.push('--append-system-prompt', options.systemPrompt);
    }

    return new Promise((resolve, reject) => {
      const proc = spawn(this.binaryPath, args, {
        env: { ...process.env, CLAUDECODE: '' },
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
          reject(new Error(`Claude Code exited with code ${code}: ${stderr}`));
          return;
        }

        try {
          const data = JSON.parse(stdout) as {
            result: string;
            cost_usd?: number;
            duration_ms?: number;
            num_turns?: number;
            session_id?: string;
          };

          resolve({
            content: data.result ?? stdout,
            tokens: { input: 0, output: 0 },
            model: this.model,
            duration,
          });
        } catch {
          // If JSON parsing fails, return raw stdout
          resolve({
            content: stdout.trim(),
            tokens: { input: 0, output: 0 },
            model: this.model,
            duration,
          });
        }
      });

      proc.on('error', (err) => {
        reject(new Error(`Failed to spawn claude: ${err.message}`));
      });
    });
  }

  async *stream(prompt: string, options?: BridgeOptions): AsyncIterable<BridgeChunk> {
    const args = [
      '-p', prompt,
      '--output-format', 'stream-json',
      '--model', this.model,
      '--max-turns', '1',
      '--no-session-persistence',
    ];

    if (options?.systemPrompt) {
      args.push('--append-system-prompt', options.systemPrompt);
    }

    const proc = spawn(this.binaryPath, args, {
      env: { ...process.env, CLAUDECODE: '' },
      stdio: ['ignore', 'pipe', 'pipe'],
      signal: options?.signal,
    });

    const reader = proc.stdout;
    let buffer = '';

    for await (const chunk of reader) {
      buffer += chunk.toString();
      const lines = buffer.split('\n');
      buffer = lines.pop() ?? '';

      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const parsed = JSON.parse(line) as {
            type: string;
            content?: string;
            message?: { content?: Array<{ text?: string }> };
          };

          if (parsed.type === 'assistant' && parsed.message?.content) {
            for (const block of parsed.message.content) {
              if (block.text) {
                yield { type: 'text', content: block.text };
              }
            }
          } else if (parsed.type === 'result') {
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
