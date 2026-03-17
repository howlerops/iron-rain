import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { SlotName } from "@howlerops/iron-rain";
import {
  buildReviewPrompt,
  generateRepoMap,
  getBranchDiff,
  getStagedDiff,
  loadIgnoreRules,
  SkillExecutor,
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
        "\n\n**@cortex/@scout/@forge** — Route to a specific slot",
    );
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

  if (command === "/init") {
    addSystemMessage("Analyzing project structure...");

    const cwd = process.cwd();
    const ignoreFilter = loadIgnoreRules(cwd);
    const repoMap = generateRepoMap(cwd, ignoreFilter, 4000);

    // Gather project metadata
    const meta: string[] = [];
    const pkgPath = join(cwd, "package.json");
    if (existsSync(pkgPath)) {
      try {
        const pkg = JSON.parse(readFileSync(pkgPath, "utf-8"));
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

    // Check for key config files
    const configFiles = [
      "tsconfig.json",
      "biome.json",
      ".eslintrc.js",
      "turbo.json",
      "Dockerfile",
      "docker-compose.yml",
      ".github/workflows/ci.yml",
    ];
    const found = configFiles.filter((f) => existsSync(join(cwd, f)));
    if (found.length > 0) {
      meta.push(`**Config files:** ${found.join(", ")}`);
    }

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
    const skillArgs = text.slice(skill.command!.length).trim();
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
