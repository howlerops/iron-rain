import { spawn } from 'node:child_process';
import { appendFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import type { CLIBridge, BridgeOptions, BridgeResult, BridgeChunk, ChatMessage } from './types.js';

const DEBUG_LOG = join(homedir(), '.iron-rain', 'debug.log');

function debugLog(msg: string) {
  try {
    mkdirSync(join(homedir(), '.iron-rain'), { recursive: true });
    appendFileSync(DEBUG_LOG, `[${new Date().toISOString()}] ${msg}\n`);
  } catch {}
}

export class ClaudeCodeBridge implements CLIBridge {
  readonly name = 'claude-code';
  private model: string;
  private binaryPath: string;
  private sessionId: string | null = null;

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
    const useResume = this.sessionId && options?.conversationHistory?.length;
    const args = [
      '-p', prompt,
      '--output-format', 'json',
      '--model', this.model,
      ...(useResume ? ['--resume', this.sessionId!] : ['--no-session-persistence']),
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

          if (data.session_id) this.sessionId = data.session_id;
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
    const useResume = this.sessionId && options?.conversationHistory?.length;
    const args = [
      '-p', prompt,
      '--output-format', 'stream-json',
      '--verbose',
      '--model', this.model,
      ...(useResume ? ['--resume', this.sessionId!] : ['--no-session-persistence']),
    ];

    if (options?.systemPrompt) {
      args.push('--append-system-prompt', options.systemPrompt);
    }

    debugLog(`stream: spawning ${this.binaryPath} ${args.join(' ')}`);

    const proc = spawn(this.binaryPath, args, {
      env: { ...process.env, CLAUDECODE: '' },
      stdio: ['ignore', 'pipe', 'pipe'],
      signal: options?.signal,
    });

    let stderrBuf = '';
    proc.stderr.on('data', (chunk: Buffer) => {
      stderrBuf += chunk.toString();
    });
    proc.on('close', (code) => {
      debugLog(`stream: process exited code=${code} stderr=${stderrBuf.slice(0, 500)}`);
    });

    const reader = proc.stdout;
    let buffer = '';
    let chunkCount = 0;
    let yieldCount = 0;

    for await (const chunk of reader) {
      buffer += chunk.toString();
      const lines = buffer.split('\n');
      buffer = lines.pop() ?? '';

      for (const line of lines) {
        if (!line.trim()) continue;
        chunkCount++;
        try {
          const parsed = JSON.parse(line) as Record<string, any>;
          debugLog(`stream: line ${chunkCount} type=${parsed.type}`);

          if (parsed.type === 'assistant' && parsed.message?.content) {
            // Claude CLI assistant messages have content as array of blocks
            const content = parsed.message.content;
            if (Array.isArray(content)) {
              for (const block of content) {
                if (block.type === 'text' && block.text) {
                  debugLog(`stream: yielding text (${block.text.length} chars)`);
                  yieldCount++;
                  yield { type: 'text', content: block.text };
                }
              }
            }
          } else if (parsed.type === 'content_block_delta' && parsed.delta?.text) {
            yieldCount++;
            yield { type: 'text', content: parsed.delta.text };
          } else if (parsed.type === 'result') {
            if (parsed.session_id) this.sessionId = parsed.session_id;
            debugLog(`stream: result event, subtype=${parsed.subtype}, yieldCount=${yieldCount}`);
            if (yieldCount === 0 && parsed.result && typeof parsed.result === 'string') {
              yield { type: 'text', content: parsed.result };
            }
            yield { type: 'done', content: '' };
            return;
          }
        } catch (e: any) {
          debugLog(`stream: parse error: ${e.message}`);
        }
      }
    }

    debugLog(`stream: stdout ended, chunkCount=${chunkCount} yieldCount=${yieldCount}`);
    yield { type: 'done', content: '' };
  }
}
