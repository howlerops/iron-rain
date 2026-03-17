import { execSync } from "node:child_process";

export function autoCommit(message: string): string | undefined {
  try {
    execSync("git add -A", { stdio: "pipe" });
    execSync(
      `git commit -m ${JSON.stringify(`iron-rain: ${message}`)} --allow-empty`,
      { stdio: "pipe" },
    );
    return execSync("git rev-parse --short HEAD", { stdio: "pipe" })
      .toString()
      .trim();
  } catch {
    return undefined;
  }
}

export function isGitRepo(): boolean {
  try {
    execSync("git rev-parse --is-inside-work-tree", { stdio: "pipe" });
    return true;
  } catch {
    return false;
  }
}

export function getChangedFiles(): string[] {
  try {
    const output = execSync("git diff --name-only", { stdio: "pipe" })
      .toString()
      .trim();
    return output ? output.split("\n").filter(Boolean) : [];
  } catch {
    return [];
  }
}

export function stashCreate(message: string): string | undefined {
  try {
    const output = execSync(`git stash push -u -m ${JSON.stringify(message)}`, {
      stdio: "pipe",
    })
      .toString()
      .trim();
    return output || undefined;
  } catch {
    return undefined;
  }
}

export function stashPop(): boolean {
  try {
    execSync("git stash pop", { stdio: "pipe" });
    return true;
  } catch {
    return false;
  }
}

export function getCurrentBranch(): string {
  try {
    return execSync("git rev-parse --abbrev-ref HEAD", { stdio: "pipe" })
      .toString()
      .trim();
  } catch {
    return "";
  }
}

export function commitIfChanged(
  message: string,
  prefix = "iron-rain",
): string | undefined {
  try {
    const status = execSync("git status --porcelain", { stdio: "pipe" })
      .toString()
      .trim();
    if (!status) return undefined;

    execSync("git add -A", { stdio: "pipe" });
    const commitMessage = `${prefix}: ${message}`;
    execSync(`git commit -m ${JSON.stringify(commitMessage)}`, {
      stdio: "pipe",
    });
    return execSync("git rev-parse --short HEAD", { stdio: "pipe" })
      .toString()
      .trim();
  } catch {
    return undefined;
  }
}
