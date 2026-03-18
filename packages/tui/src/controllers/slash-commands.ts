import { execFile, execSync } from "node:child_process";
import { existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { SlotName } from "@howlerops/iron-rain";
import {
  buildReviewPrompt,
  generateRepoMap,
  getBranchDiff,
  getCurrentBranch,
  getStagedDiff,
  isGitRepo,
  loadConfig,
  loadIgnoreRules,
  SkillExecutor,
  writeConfig,
} from "@howlerops/iron-rain";
import { SLASH_COMMANDS } from "../components/slash-menu.js";
import { getSessionDB } from "../context/slate-context.js";
import type { SessionContext } from "./context.js";

function levenshtein(a: string, b: string): number {
  const m = a.length;
  const n = b.length;
  const dp: number[][] = Array.from({ length: m + 1 }, () =>
    Array(n + 1).fill(0),
  );
  for (let i = 0; i <= m; i++) dp[i][0] = i;
  for (let j = 0; j <= n; j++) dp[0][j] = j;
  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      dp[i][j] =
        a[i - 1] === b[j - 1]
          ? dp[i - 1][j - 1]
          : 1 + Math.min(dp[i - 1][j], dp[i][j - 1], dp[i - 1][j - 1]);
    }
  }
  return dp[m][n];
}

const CONFIG_FILES = [
  "tsconfig.json",
  "biome.json",
  ".eslintrc.js",
  "turbo.json",
  "Dockerfile",
  "docker-compose.yml",
  ".github/workflows/ci.yml",
];

async function gatherProjectMeta(cwd: string): Promise<string[]> {
  const meta: string[] = [];

  // Read package.json and check config files in parallel
  const [pkgResult, ...configResults] = await Promise.allSettled([
    readFile(join(cwd, "package.json"), "utf-8"),
    ...CONFIG_FILES.map((f) => readFile(join(cwd, f), "utf-8").then(() => f)),
  ]);

  if (pkgResult.status === "fulfilled") {
    try {
      const pkg = JSON.parse(pkgResult.value);
      const deps = Object.keys(pkg.dependencies ?? {});
      const devDeps = Object.keys(pkg.devDependencies ?? {});
      meta.push(
        `**Package:** ${pkg.name ?? "unknown"} v${pkg.version ?? "0.0.0"}`,
        `**Dependencies:** ${deps.slice(0, 15).join(", ")}${deps.length > 15 ? ` (+${deps.length - 15} more)` : ""}`,
        `**Dev Dependencies:** ${devDeps.slice(0, 10).join(", ")}${devDeps.length > 10 ? ` (+${devDeps.length - 10} more)` : ""}`,
      );
    } catch {
      /* skip */
    }
  }

  const found = configResults
    .filter(
      (r): r is PromiseFulfilledResult<string> => r.status === "fulfilled",
    )
    .map((r) => r.value);
  if (found.length > 0) {
    meta.push(`**Config files:** ${found.join(", ")}`);
  }

  return meta;
}

function copyToClipboard(text: string): Promise<void> {
  const cmd =
    process.platform === "darwin"
      ? "pbcopy"
      : process.platform === "win32"
        ? "clip"
        : "xclip";
  const args = process.platform === "linux" ? ["-selection", "clipboard"] : [];

  return new Promise((resolve, reject) => {
    const proc = execFile(cmd, args, (err) => {
      if (err) reject(err);
      else resolve();
    });
    proc.stdin?.write(text);
    proc.stdin?.end();
  });
}

function git(cmd: string): string {
  return execSync(`git ${cmd}`, { stdio: "pipe" }).toString().trim();
}

function runShell(
  cmd: string,
): Promise<{ stdout: string; stderr: string; code: number }> {
  return new Promise((resolve) => {
    execFile(
      "sh",
      ["-c", cmd],
      { maxBuffer: 1024 * 1024 },
      (err, stdout, stderr) => {
        resolve({
          stdout: stdout?.toString() ?? "",
          stderr: stderr?.toString() ?? "",
          code: err ? ((err as any).code ?? 1) : 0,
        });
      },
    );
  });
}

function detectTestCommand(): string {
  const cwd = process.cwd();
  if (
    existsSync(join(cwd, "bun.lockb")) ||
    existsSync(join(cwd, "bunfig.toml"))
  )
    return "bun test";
  if (existsSync(join(cwd, "package.json"))) {
    try {
      const pkg = JSON.parse(
        require("node:fs").readFileSync(join(cwd, "package.json"), "utf-8"),
      );
      if (pkg.scripts?.test) return "npm test";
    } catch {}
  }
  if (existsSync(join(cwd, "Cargo.toml"))) return "cargo test";
  if (existsSync(join(cwd, "go.mod"))) return "go test ./...";
  if (
    existsSync(join(cwd, "pyproject.toml")) ||
    existsSync(join(cwd, "setup.py"))
  )
    return "python -m pytest";
  return "npm test";
}

export async function handleBasicSlashCommand(
  command: string,
  args: string[],
  context: SessionContext,
): Promise<boolean> {
  const { actions, addSystemMessage, setMode, onQuit, skillCommands, state } =
    context;

  if (command === "/quit" || command === "/exit") {
    onQuit?.();
    return true;
  }

  if (command === "/clear") {
    actions.clearMessages();
    return true;
  }

  if (command === "/settings") {
    setMode("settings");
    return true;
  }

  if (command === "/new") {
    actions.newSession();
    return true;
  }

  if (command === "/help") {
    const allCmds = [...SLASH_COMMANDS, ...skillCommands()];
    addSystemMessage(
      allCmds.map((c) => `**${c.name}** — ${c.description}`).join("\n") +
        "\n\n**@cortex/@scout/@forge** — Route to a specific slot" +
        "\n\n**Tip:** Hold **Shift** and drag to select text in the terminal for copying.",
    );
    return true;
  }

  if (command === "/copy") {
    const assistantMsgs = state.messages.filter((m) => m.role === "assistant");
    if (assistantMsgs.length === 0) {
      addSystemMessage("No assistant messages to copy.");
      return true;
    }
    const last = assistantMsgs[assistantMsgs.length - 1];
    try {
      await copyToClipboard(last.content);
      addSystemMessage("Copied last response to clipboard.");
    } catch {
      addSystemMessage("Failed to copy — clipboard not available.");
    }
    return true;
  }

  if (command === "/lessons") {
    const db = getSessionDB();
    if (!db) {
      addSystemMessage("No database available for lessons.");
    } else {
      const lessons = db.getLessons(20);
      const content =
        lessons.length === 0
          ? "No lessons learned yet. Lessons are saved from conversations to improve future responses."
          : lessons
              .map(
                (l: any, i: number) =>
                  `${i + 1}. ${l.content}${l.tags.length ? ` *(${l.tags.join(", ")})*` : ""}`,
              )
              .join("\n");
      addSystemMessage(`## Lessons Learned\n${content}`);
    }
    return true;
  }

  if (command === "/context" && (args.length === 0 || args[0] === "help")) {
    addSystemMessage(
      "## Context Directories\n" +
        "`/context add <path>` — Add a directory to context scope\n" +
        "`/context list` — Show current context directories\n" +
        "`/context remove <path>` — Remove a directory from context scope",
    );
    return true;
  }

  if (command === "/context" && args[0] === "add") {
    const dirPath = args.slice(1).join(" ").trim();
    if (!dirPath) {
      addSystemMessage("Usage: `/context add <path>`");
      return true;
    }
    const err = actions.addContextDirectory(dirPath);
    if (err) {
      addSystemMessage(`**Error:** ${err}`);
    } else {
      addSystemMessage(`Added context directory: \`${dirPath}\``);
    }
    return true;
  }

  if (command === "/context" && args[0] === "list") {
    const dirs = actions.contextDirectories();
    if (dirs.length === 0) {
      addSystemMessage(
        "No context directories configured. Use `/context add <path>` to add one.",
      );
    } else {
      addSystemMessage(
        `## Context Directories\n${dirs.map((d) => `- \`${d}\``).join("\n")}`,
      );
    }
    return true;
  }

  if (command === "/context" && args[0] === "remove") {
    const dirPath = args.slice(1).join(" ").trim();
    if (!dirPath) {
      addSystemMessage("Usage: `/context remove <path>`");
      return true;
    }
    const removed = actions.removeContextDirectory(dirPath);
    if (removed) {
      addSystemMessage(`Removed context directory: \`${dirPath}\``);
    } else {
      addSystemMessage(`Directory not found in context: \`${dirPath}\``);
    }
    return true;
  }

  if (command === "/skills") {
    const skills = actions.skillRegistry().list();
    if (skills.length === 0) {
      addSystemMessage(
        "No skills found. Add .md files to `.iron-rain/skills/` or `.claude/skills/`.",
      );
    } else {
      const grouped = actions.skillRegistry().grouped();
      const parts: string[] = ["## Available Skills"];
      for (const [source, sourceSkills] of grouped) {
        parts.push(`\n### ${source}`);
        for (const s of sourceSkills) {
          parts.push(`- **${s.command}** — ${s.description}`);
        }
      }
      addSystemMessage(parts.join("\n"));
    }
    return true;
  }

  if (command === "/mcp") {
    const mgr = actions.mcpManager();
    const status = mgr.getStatus();
    if (status.length === 0) {
      addSystemMessage(
        "No MCP servers configured. Add `mcpServers` to your iron-rain.json config.",
      );
    } else {
      const lines = status.map((s) => {
        const icon = s.connected ? "\u2713" : "\u2717";
        return `${icon} **${s.name}**: ${s.connected ? `Connected (${s.toolCount} tools)` : "Disconnected"}`;
      });
      addSystemMessage(
        `## MCP Servers\n${lines.join("\n")}\n\nTotal tools: ${mgr.totalToolCount}`,
      );
    }
    return true;
  }

  if (command === "/model") {
    const lines = Object.entries(state.slots).map(
      ([slot, cfg]) => `- **${slot}**: ${cfg.model}`,
    );
    addSystemMessage(`## Models\n${lines.join("\n")}`);
    return true;
  }

  if (command === "/slot") {
    const maybeSlot = args[0] as SlotName | undefined;
    if (!maybeSlot) {
      addSystemMessage(`Active slot: **${actions.activeSlot()}**`);
      return true;
    }

    if (maybeSlot in state.slots) {
      actions.setActiveSlot(maybeSlot);
      addSystemMessage(`Active slot set to **${maybeSlot}**`);
    } else {
      addSystemMessage(`Unknown slot: **${maybeSlot}**`);
    }
    return true;
  }

  if (command === "/undo") {
    const result = actions.undo();
    if (result.success) {
      addSystemMessage(`Restored checkpoint: **${result.label ?? "latest"}**`);
    } else {
      addSystemMessage("No checkpoints available to undo.");
    }
    return true;
  }

  if (command === "/review") {
    const target = args[0];
    const diff = target ? getBranchDiff(target) : getStagedDiff();
    if (!diff.trim()) {
      addSystemMessage(
        "No changes to review. Stage changes with `git add` or specify a branch: `/review main`",
      );
      return true;
    }
    const prompt = buildReviewPrompt(diff);
    await actions.dispatch(prompt);
    return true;
  }

  if (command === "/commit") {
    if (!isGitRepo()) {
      addSystemMessage("Not a git repository.");
      return true;
    }
    try {
      const status = git("status --porcelain");
      if (!status) {
        addSystemMessage("Nothing to commit — working tree clean.");
        return true;
      }
      const diff = git("diff HEAD");
      const branch = getCurrentBranch();
      addSystemMessage("Generating commit message...");
      const prompt = [
        "Generate a concise, conventional commit message for these changes.",
        "Use the format: `type(scope): description` (e.g. feat, fix, refactor, docs, chore).",
        "Return ONLY the commit message, nothing else. No markdown, no explanation.",
        "",
        `Branch: ${branch}`,
        "```diff",
        diff.slice(0, 8000),
        "```",
      ].join("\n");
      await actions.dispatch(prompt);

      // After the model responds, extract the message from the last assistant reply
      const lastMsg = state.messages
        .filter((m) => m.role === "assistant")
        .pop();
      if (lastMsg) {
        const msg = lastMsg.content.replace(/^```\s*|```$/g, "").trim();
        git("add -A");
        git(`commit -m ${JSON.stringify(msg)}`);
        try {
          git(`push origin ${branch}`);
          addSystemMessage(`Committed and pushed to **${branch}**.`);
        } catch {
          addSystemMessage(
            `Committed locally. Push failed — run \`git push\` manually.`,
          );
        }
      }
    } catch (err) {
      addSystemMessage(
        `**Error:** ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return true;
  }

  if (command === "/diff") {
    if (!isGitRepo()) {
      addSystemMessage("Not a git repository.");
      return true;
    }
    try {
      const staged = git("diff --cached");
      const unstaged = git("diff");
      if (!staged && !unstaged) {
        addSystemMessage("No changes. Working tree is clean.");
        return true;
      }
      const parts: string[] = [];
      if (staged)
        parts.push(`## Staged Changes\n\`\`\`diff\n${staged}\n\`\`\``);
      if (unstaged)
        parts.push(`## Unstaged Changes\n\`\`\`diff\n${unstaged}\n\`\`\``);
      addSystemMessage(parts.join("\n\n"));
    } catch (err) {
      addSystemMessage(
        `**Error:** ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return true;
  }

  if (command === "/branch") {
    if (!isGitRepo()) {
      addSystemMessage("Not a git repository.");
      return true;
    }
    try {
      if (args.length === 0) {
        const current = getCurrentBranch();
        const branches = git("branch --sort=-committerdate")
          .split("\n")
          .slice(0, 10)
          .map((b) => b.trim());
        addSystemMessage(
          `## Branches\nCurrent: **${current}**\n\n${branches.map((b) => `- ${b}`).join("\n")}`,
        );
      } else if (args[0] === "new" && args[1]) {
        git(`checkout -b ${args[1]}`);
        addSystemMessage(`Created and switched to **${args[1]}**.`);
      } else {
        git(`checkout ${args[0]}`);
        addSystemMessage(`Switched to **${args[0]}**.`);
      }
    } catch (err) {
      addSystemMessage(
        `**Error:** ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return true;
  }

  if (command === "/test") {
    const testCmd = args.length > 0 ? args.join(" ") : detectTestCommand();
    addSystemMessage(`Running \`${testCmd}\`...`);
    const result = await runShell(testCmd);
    const output = (result.stdout + result.stderr).trim();
    const status = result.code === 0 ? "PASSED" : "FAILED";
    const truncated =
      output.length > 4000 ? `...${output.slice(-4000)}` : output;
    addSystemMessage(
      `## Test Results — ${status}\n\`\`\`\n${truncated}\n\`\`\``,
    );
    return true;
  }

  if (command === "/init") {
    addSystemMessage("Analyzing project structure...");

    const cwd = process.cwd();
    const ignoreFilter = loadIgnoreRules(cwd);

    // Run repo map generation and metadata gathering in parallel
    const [repoMap, meta] = await Promise.all([
      generateRepoMap(cwd, ignoreFilter, 4000),
      gatherProjectMeta(cwd),
    ]);

    // Store structural findings as lessons
    const db = getSessionDB();
    if (db) {
      db.addLesson(
        `Project structure (${repoMap.split("\n").length} files mapped):\n${repoMap.slice(0, 2000)}`,
        "/init",
        ["structure", "init", "architecture"],
      );
      if (meta.length > 0) {
        db.addLesson(meta.join("\n"), "/init", [
          "dependencies",
          "init",
          "tech-stack",
        ]);
      }
    }

    // Dispatch architecture review to the model
    const prompt = [
      "Analyze this project and provide a concise report with these sections:",
      "",
      "1. **Tech Stack** — languages, frameworks, key dependencies",
      "2. **Architecture** — how the codebase is organized, main patterns used",
      "3. **Conventions** — coding style, naming patterns, file organization rules",
      "4. **Recommendations** — ONLY if there are genuine improvements to suggest. If the architecture is sound, explicitly say so and skip this section.",
      "",
      "## Repository Map",
      repoMap,
      "",
      meta.length > 0 ? `## Project Metadata\n${meta.join("\n")}` : "",
      "",
      "Be concise and actionable. Do NOT suggest changes unless they would meaningfully improve the codebase.",
    ]
      .filter(Boolean)
      .join("\n");

    await actions.dispatch(prompt);
    return true;
  }

  if (command === "/permissions") {
    const config = loadConfig();
    const cliProviders = ["claude-code", "codex", "gemini-cli"];
    const current = config.cliPermissions ?? {};

    // If any arg given, treat as toggle target
    if (args.length > 0 && args[0] === "off") {
      // Set all to supervised
      const updated: Record<string, "auto" | "supervised"> = {};
      for (const p of cliProviders) updated[p] = "supervised";
      config.cliPermissions = updated;
      writeConfig(config);
      actions.getDispatcher().setCliPermissions(updated);
      actions.setCliAutoMode(false);
      addSystemMessage(
        "CLI permissions set to **supervised** (agents will ask before editing).",
      );
      return true;
    }

    // Default: toggle all to auto or supervised
    const allAuto = cliProviders.every((p) => current[p] === "auto");
    const newMode = allAuto ? "supervised" : "auto";
    const updated: Record<string, "auto" | "supervised"> = {};
    for (const p of cliProviders) updated[p] = newMode;
    config.cliPermissions = updated;
    writeConfig(config);
    actions.getDispatcher().setCliPermissions(updated);
    actions.setCliAutoMode(newMode === "auto");

    const label =
      newMode === "auto"
        ? "**auto** (agents can edit files without asking)"
        : "**supervised** (agents will ask before editing)";
    addSystemMessage(`CLI permissions set to ${label}.`);
    return true;
  }

  if (command === "/stats") {
    const stats = state.sessionStats;
    addSystemMessage(
      `## Session Stats\n` +
        `Requests: ${stats.requestCount}\n` +
        `Total tokens: ${stats.totalTokens}\n` +
        `Total duration: ${stats.totalDuration}ms`,
    );
    return true;
  }

  return false;
}

export async function handleSlashCommand(
  text: string,
  context: SessionContext,
): Promise<boolean> {
  const [command, ...args] = text.trim().split(/\s+/);

  if (await handleBasicSlashCommand(command, args, context)) {
    return true;
  }

  const skill = context.actions.skillRegistry().getByCommand(command);
  if (skill) {
    const skillArgs = text.slice(skill.command?.length).trim();
    context.addSystemMessage(`Running skill: **${skill.name}**...`);
    const kernel = context.actions
      .getDispatcher()
      .ensureKernel(context.state.slots);
    const executor = new SkillExecutor(kernel);
    try {
      const episode = await executor.execute(skill, skillArgs || undefined);
      context.addSystemMessage(episode.result);
    } catch (err) {
      context.addSystemMessage(
        `Skill error: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return true;
  }

  // Unknown command — suggest closest match
  const allCmds = [...SLASH_COMMANDS, ...(context.skillCommands?.() ?? [])];
  const suggestions = allCmds
    .map((c) => ({
      name: c.name,
      description: c.description,
      dist: levenshtein(command, c.name),
    }))
    .filter((s) => s.dist <= 3)
    .sort((a, b) => a.dist - b.dist);

  if (suggestions.length > 0) {
    const best = suggestions[0];
    context.addSystemMessage(
      `Unknown command: **${command}**. Did you mean **${best.name}**? *(${best.description})*`,
    );
  } else {
    context.addSystemMessage(
      `Unknown command: **${command}**. Type **/help** for available commands.`,
    );
  }

  return true;
}
