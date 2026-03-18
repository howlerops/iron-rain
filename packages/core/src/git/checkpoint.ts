import { execSync } from "node:child_process";
import { isGitRepo } from "./utils.js";

export interface Checkpoint {
  id: string;
  label: string;
  timestamp: number;
  commitRef: string;
}

/**
 * Manages lightweight checkpoints using git commits on a shadow branch.
 * Checkpoints allow undo/restore of working tree state.
 */
export class CheckpointManager {
  private stack: Checkpoint[] = [];
  private enabled = false;

  constructor() {
    this.enabled = isGitRepo();
  }

  get isEnabled(): boolean {
    return this.enabled;
  }

  /**
   * Create a checkpoint of the current working tree state.
   */
  createCheckpoint(label: string): string | undefined {
    if (!this.enabled) return undefined;

    try {
      // Stage everything and create a temporary commit for the stash ref
      const ref = execSync("git stash create", { stdio: "pipe" })
        .toString()
        .trim();

      // If no changes, stash create returns empty
      const commitRef = ref || this.getCurrentHead();
      const id = `cp-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;

      this.stack.push({
        id,
        label,
        timestamp: Date.now(),
        commitRef,
      });

      return id;
    } catch {
      return undefined;
    }
  }

  /**
   * Restore the last checkpoint, undoing changes made since it was created.
   */
  restoreLastCheckpoint(): { success: boolean; label?: string } {
    if (!this.enabled || this.stack.length === 0) {
      return { success: false };
    }

    const checkpoint = this.stack.pop()!;

    try {
      if (checkpoint.commitRef) {
        // Reset working tree to the checkpoint state
        execSync("git checkout -- .", { stdio: "pipe" });
        execSync("git clean -fd", { stdio: "pipe" });

        // If we had a stash ref, apply it
        if (checkpoint.commitRef !== this.getCurrentHead()) {
          try {
            execSync(`git stash apply ${checkpoint.commitRef}`, {
              stdio: "pipe",
            });
          } catch {
            // Stash may have been cleaned up - that's okay, checkout already reset
          }
        }
      }
      return { success: true, label: checkpoint.label };
    } catch {
      return { success: false, label: checkpoint.label };
    }
  }

  /**
   * List all checkpoints in the stack.
   */
  listCheckpoints(): readonly Checkpoint[] {
    return this.stack;
  }

  /**
   * Remove checkpoints older than maxAge milliseconds.
   */
  pruneOldCheckpoints(maxAgeMs: number): number {
    const cutoff = Date.now() - maxAgeMs;
    const before = this.stack.length;
    this.stack = this.stack.filter((cp) => cp.timestamp > cutoff);
    return before - this.stack.length;
  }

  /**
   * Clear all checkpoints.
   */
  clear(): void {
    this.stack = [];
  }

  private getCurrentHead(): string {
    try {
      return execSync("git rev-parse HEAD", { stdio: "pipe" })
        .toString()
        .trim();
    } catch {
      return "";
    }
  }
}
