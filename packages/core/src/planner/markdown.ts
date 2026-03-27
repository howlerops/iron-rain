/**
 * Markdown plan serialization — round-trippable Plan <-> Markdown.
 * YAML frontmatter + structured markdown with tasks as ### subsections.
 */
import type { Plan, PlanTask } from "./types.js";

/**
 * Serialize a Plan object to a human-readable markdown string.
 */
export function serializePlanToMarkdown(plan: Plan): string {
  const lines: string[] = [];

  // YAML frontmatter
  lines.push("---");
  lines.push(`id: ${plan.id}`);
  lines.push(`status: ${plan.status}`);
  lines.push(`autoCommit: ${plan.autoCommit}`);
  lines.push(`createdAt: ${plan.createdAt}`);
  lines.push(`updatedAt: ${plan.updatedAt}`);
  lines.push("---");
  lines.push("");

  // Title
  lines.push(`# ${plan.title}`);
  lines.push("");

  // Description
  if (plan.description) {
    lines.push(plan.description);
    lines.push("");
  }

  // PRD
  if (plan.prd) {
    lines.push("## PRD");
    lines.push("");
    lines.push(plan.prd);
    lines.push("");
  }

  // Tasks
  if (plan.tasks.length > 0) {
    lines.push("## Tasks");
    lines.push("");

    for (const task of plan.tasks) {
      lines.push(`### Task ${task.index}: ${task.title}`);
      lines.push(`**Status:** ${task.status}`);

      if (task.targetFiles && task.targetFiles.length > 0) {
        lines.push(`**Files:** ${task.targetFiles.join(", ")}`);
      }

      if (task.dependsOn && task.dependsOn.length > 0) {
        lines.push(`**Depends on:** ${task.dependsOn.join(", ")}`);
      }

      lines.push("");

      if (task.description) {
        lines.push(task.description);
        lines.push("");
      }

      if (task.acceptanceCriteria.length > 0) {
        lines.push("**Acceptance Criteria:**");
        for (const criterion of task.acceptanceCriteria) {
          const checked = task.status === "completed" ? "x" : " ";
          lines.push(`- [${checked}] ${criterion}`);
        }
        lines.push("");
      }
    }
  }

  return `${lines.join("\n").trimEnd()}\n`;
}

/**
 * Parse a markdown plan string back into a Plan object.
 */
export function parsePlanFromMarkdown(content: string): Plan {
  // Extract YAML frontmatter
  const fmMatch = content.match(/^---\n([\s\S]*?)\n---/);
  const frontmatter: Record<string, string> = {};

  if (fmMatch) {
    for (const line of fmMatch[1].split("\n")) {
      const colonIdx = line.indexOf(":");
      if (colonIdx > 0) {
        const key = line.slice(0, colonIdx).trim();
        const value = line.slice(colonIdx + 1).trim();
        frontmatter[key] = value;
      }
    }
  }

  // Strip frontmatter from content
  const body = fmMatch
    ? content.slice(fmMatch[0].length).trim()
    : content.trim();

  // Extract title from first H1
  const titleMatch = body.match(/^# (.+)$/m);
  const title = titleMatch ? titleMatch[1].trim() : "Untitled Plan";

  // Split body into sections
  const afterTitle = titleMatch
    ? body.slice(body.indexOf(titleMatch[0]) + titleMatch[0].length).trim()
    : body;

  // Extract PRD section
  const prdMatch = afterTitle.match(/^## PRD\n([\s\S]*?)(?=\n## |\n*$)/m);
  const prd = prdMatch ? prdMatch[1].trim() : "";

  // Extract description (text between title and first ## section)
  const firstSection = afterTitle.match(/^## /m);
  const description = firstSection
    ? afterTitle.slice(0, firstSection.index).trim()
    : prd
      ? ""
      : afterTitle.trim();

  // Extract tasks
  const tasks: PlanTask[] = [];
  const tasksMatch = afterTitle.match(
    /^## Tasks\n([\s\S]*?)(?=\n## (?!#)|\n*$)/m,
  );

  if (tasksMatch) {
    const tasksBody = tasksMatch[1];
    const taskBlocks = tasksBody
      .split(/(?=^### Task )/m)
      .filter((b) => b.trim());

    for (const block of taskBlocks) {
      const headerMatch = block.match(/^### Task (\d+):\s*(.+)$/m);
      if (!headerMatch) continue;

      const taskIndex = Number.parseInt(headerMatch[1], 10);
      const taskTitle = headerMatch[2].trim();

      const statusMatch = block.match(/^\*\*Status:\*\*\s*(.+)$/m);
      const filesMatch = block.match(/^\*\*Files:\*\*\s*(.+)$/m);
      const dependsMatch = block.match(/^\*\*Depends on:\*\*\s*(.+)$/m);

      // Extract acceptance criteria
      const criteria: string[] = [];
      const criteriaMatch = block.match(
        /\*\*Acceptance Criteria:\*\*\n((?:- \[[ x]\] .+\n?)*)/,
      );
      if (criteriaMatch) {
        for (const line of criteriaMatch[1].split("\n")) {
          const cm = line.match(/- \[[ x]\] (.+)/);
          if (cm) criteria.push(cm[1]);
        }
      }

      // Extract description: text between metadata lines and acceptance criteria
      const metaEnd = block.search(/^\n/m);
      const descStart = metaEnd >= 0 ? metaEnd : 0;
      const acStart = block.indexOf("**Acceptance Criteria:**");
      const descBlock = block
        .slice(descStart, acStart >= 0 ? acStart : undefined)
        .replace(/^\*\*(?:Status|Files|Depends on):\*\*.+$/gm, "")
        .replace(/^### Task .+$/m, "")
        .trim();

      tasks.push({
        id: `task-${taskIndex}`,
        index: taskIndex,
        title: taskTitle,
        description: descBlock,
        status: (statusMatch?.[1].trim() ?? "pending") as PlanTask["status"],
        targetFiles: filesMatch
          ? filesMatch[1].split(",").map((f) => f.trim())
          : undefined,
        dependsOn: dependsMatch
          ? dependsMatch[1].split(",").map((d) => d.trim())
          : undefined,
        acceptanceCriteria: criteria,
      });
    }
  }

  const now = Date.now();

  return {
    id: frontmatter.id || `plan-${now}`,
    title,
    description,
    prd,
    tasks,
    status: (frontmatter.status ?? "review") as Plan["status"],
    autoCommit: frontmatter.autoCommit === "true",
    createdAt: frontmatter.createdAt ? Number(frontmatter.createdAt) : now,
    updatedAt: frontmatter.updatedAt ? Number(frontmatter.updatedAt) : now,
    stats: {
      tasksCompleted: 0,
      tasksFailed: 0,
      totalDuration: 0,
      totalTokens: 0,
    },
  };
}
