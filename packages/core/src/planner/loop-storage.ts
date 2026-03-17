import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import type { LoopState } from "./types.js";

const LOOPS_DIR = ".iron-rain/loops";

/**
 * Persistent storage for loop state, mirroring PlanStorage API.
 */
export class LoopStorage {
  private baseDir: string;

  constructor(cwd?: string) {
    this.baseDir = join(cwd ?? process.cwd(), LOOPS_DIR);
  }

  save(id: string, state: LoopState): void {
    const dir = join(this.baseDir, id);
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      join(dir, "loop.json"),
      JSON.stringify(state, null, 2),
      "utf-8",
    );
  }

  load(id: string): LoopState | null {
    const path = join(this.baseDir, id, "loop.json");
    if (!existsSync(path)) return null;

    try {
      return JSON.parse(readFileSync(path, "utf-8")) as LoopState;
    } catch {
      return null;
    }
  }

  list(): Array<{
    id: string;
    want: string;
    iterations: number;
    status: string;
  }> {
    if (!existsSync(this.baseDir)) return [];

    const dirs = readdirSync(this.baseDir, { withFileTypes: true })
      .filter((d) => d.isDirectory())
      .map((d) => d.name);

    const results: Array<{
      id: string;
      want: string;
      iterations: number;
      status: string;
    }> = [];

    for (const id of dirs) {
      const state = this.load(id);
      if (state) {
        results.push({
          id,
          want: state.config.want,
          iterations: state.iterations.length,
          status: state.status,
        });
      }
    }

    return results.sort((a, b) => b.iterations - a.iterations);
  }

  delete(id: string): boolean {
    const path = join(this.baseDir, id, "loop.json");
    if (!existsSync(path)) return false;

    try {
      unlinkSync(path);
      return true;
    } catch {
      return false;
    }
  }
}
