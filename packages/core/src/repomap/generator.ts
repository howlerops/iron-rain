import { readdirSync, readFileSync, statSync } from "node:fs";
import { extname, join, relative } from "node:path";
import type { IgnoreFilter } from "../context/ignore.js";

interface SymbolEntry {
  file: string;
  symbols: string[];
}

const SOURCE_EXTENSIONS = new Set([
  ".ts",
  ".tsx",
  ".js",
  ".jsx",
  ".mjs",
  ".cjs",
  ".py",
  ".rs",
  ".go",
  ".java",
  ".rb",
  ".c",
  ".cpp",
  ".h",
]);

const EXPORT_PATTERNS: Record<string, RegExp[]> = {
  typescript: [
    /export\s+(?:default\s+)?(?:class|function|const|let|var|interface|type|enum|abstract\s+class)\s+(\w+)/g,
  ],
  python: [/^(?:class|def)\s+(\w+)/gm],
  rust: [/pub\s+(?:fn|struct|enum|trait|type|mod|const)\s+(\w+)/g],
  go: [/^func\s+(\w+)/gm, /^type\s+(\w+)/gm],
};

/**
 * Generate a lightweight repo map showing files and exported symbols.
 */
export function generateRepoMap(
  cwd: string,
  ignoreFilter?: IgnoreFilter,
  maxTokens = 2000,
): string {
  const entries: SymbolEntry[] = [];
  walkDir(cwd, cwd, entries, ignoreFilter);

  // Sort by path
  entries.sort((a, b) => a.file.localeCompare(b.file));

  // Build output, respecting token budget (~4 chars per token)
  const charBudget = maxTokens * 4;
  const lines: string[] = [];
  let charCount = 0;

  for (const entry of entries) {
    const symbolStr =
      entry.symbols.length > 0 ? ` → ${entry.symbols.join(", ")}` : "";
    const line = `${entry.file}${symbolStr}`;

    if (charCount + line.length + 1 > charBudget) break;
    lines.push(line);
    charCount += line.length + 1;
  }

  return lines.join("\n");
}

function walkDir(
  dir: string,
  root: string,
  entries: SymbolEntry[],
  ignoreFilter?: IgnoreFilter,
  depth = 0,
): void {
  if (depth > 8) return; // Prevent excessive recursion

  let items: string[];
  try {
    items = readdirSync(dir);
  } catch {
    return;
  }

  for (const item of items) {
    if (
      item.startsWith(".") ||
      item === "node_modules" ||
      item === "dist" ||
      item === "build"
    ) {
      continue;
    }

    const fullPath = join(dir, item);
    if (ignoreFilter?.isIgnored(fullPath)) continue;

    let stat;
    try {
      stat = statSync(fullPath);
    } catch {
      continue;
    }

    if (stat.isDirectory()) {
      walkDir(fullPath, root, entries, ignoreFilter, depth + 1);
    } else if (stat.isFile() && SOURCE_EXTENSIONS.has(extname(item))) {
      const relPath = relative(root, fullPath);
      const symbols = extractSymbols(fullPath);
      entries.push({ file: relPath, symbols });
    }
  }
}

function extractSymbols(filePath: string): string[] {
  const ext = extname(filePath);
  let lang: string;

  if ([".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"].includes(ext)) {
    lang = "typescript";
  } else if (ext === ".py") {
    lang = "python";
  } else if (ext === ".rs") {
    lang = "rust";
  } else if (ext === ".go") {
    lang = "go";
  } else {
    return [];
  }

  const patterns = EXPORT_PATTERNS[lang];
  if (!patterns) return [];

  let content: string;
  try {
    content = readFileSync(filePath, "utf-8");
    // Only read first 10KB for symbol extraction
    if (content.length > 10240) content = content.slice(0, 10240);
  } catch {
    return [];
  }

  const symbols = new Set<string>();
  for (const pattern of patterns) {
    // Reset regex lastIndex
    const re = new RegExp(pattern.source, pattern.flags);
    let match;
    while ((match = re.exec(content)) !== null) {
      if (match[1]) symbols.add(match[1]);
    }
  }

  return [...symbols];
}
