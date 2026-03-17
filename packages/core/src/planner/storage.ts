/**
 * Plan Storage — file-based plan persistence.
 * Plans stored in .iron-rain/plans/<id>/plan.json + prd.md
 */
import * as fs from "node:fs";
import * as path from "node:path";
import type { Plan } from "./types.js";

const PLANS_DIR = ".iron-rain/plans";

function getPlansDir(): string {
  return path.join(process.cwd(), PLANS_DIR);
}

function getPlanDir(planId: string): string {
  return path.join(getPlansDir(), planId);
}

export class PlanStorage {
  save(plan: Plan): void {
    const dir = getPlanDir(plan.id);
    fs.mkdirSync(dir, { recursive: true });

    // Save plan.json (everything except prd body)
    const planJson = { ...plan };
    fs.writeFileSync(
      path.join(dir, "plan.json"),
      JSON.stringify(planJson, null, 2),
    );

    // Save prd.md separately for human readability
    fs.writeFileSync(path.join(dir, "prd.md"), plan.prd);
  }

  load(planId: string): Plan | null {
    const planPath = path.join(getPlanDir(planId), "plan.json");
    try {
      const raw = fs.readFileSync(planPath, "utf-8");
      return JSON.parse(raw) as Plan;
    } catch {
      return null;
    }
  }

  list(): Array<{
    id: string;
    title: string;
    status: string;
    createdAt: number;
  }> {
    const dir = getPlansDir();
    if (!fs.existsSync(dir)) return [];

    try {
      return fs
        .readdirSync(dir)
        .filter((entry) => {
          const planJson = path.join(dir, entry, "plan.json");
          return fs.existsSync(planJson);
        })
        .map((entry) => {
          const plan = this.load(entry);
          return plan
            ? {
                id: plan.id,
                title: plan.title,
                status: plan.status,
                createdAt: plan.createdAt,
              }
            : null;
        })
        .filter((p): p is NonNullable<typeof p> => p !== null)
        .sort((a, b) => b.createdAt - a.createdAt);
    } catch {
      return [];
    }
  }

  update(planId: string, updates: Partial<Plan>): Plan | null {
    const plan = this.load(planId);
    if (!plan) return null;

    const updated = { ...plan, ...updates, updatedAt: Date.now() };
    this.save(updated);
    return updated;
  }

  delete(planId: string): boolean {
    const dir = getPlanDir(planId);
    try {
      fs.rmSync(dir, { recursive: true, force: true });
      return true;
    } catch {
      return false;
    }
  }
}
