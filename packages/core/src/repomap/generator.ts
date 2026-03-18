import { readdir, readFile, stat } from "node:fs/promises";
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
export async function generateRepoMap(
  cwd: string,
  ignoreFilter?: IgnoreFilter,
  maxTokens = 2000,
): Promise<string> {
  const entries: SymbolEntry[] = [];
  await walkDir(cwd, cwd, entries, ignoreFilter);

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

async function walkDir(
  dir: string,
  root: string,
  entries: SymbolEntry[],
  ignoreFilter?: IgnoreFilter,
  depth = 0,
): Promise<void> {
  if (depth > 8) return; // Prevent excessive recursion

  let items: string[];
  try {
    items = await readdir(dir);
  } catch {
    return;
  }

  const filePromises: Promise<SymbolEntry | null>[] = [];
  const dirPromises: Promise<void>[] = [];

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

    // Fire all stat calls in parallel by deferring to promises
    const p = stat(fullPath)
      .then((s) => {
        if (s.isDirectory()) {
          dirPromises.push(
            walkDir(fullPath, root, entries, ignoreFilter, depth + 1),
          );
          return null;
        }
        if (s.isFile() && SOURCE_EXTENSIONS.has(extname(item))) {
          return extractSymbols(fullPath).then((symbols) => ({
            file: relative(root, fullPath),
            symbols,
          }));
        }
        return null;
      })
      .catch(() => null);

    filePromises.push(p);
  }

  // Wait for all stat calls to resolve (which also queues subdirectory walks)
  const fileResults = await Promise.all(filePromises);
  for (const entry of fileResults) {
    if (entry) entries.push(entry);
  }

  // Now wait for all subdirectory walks
  await Promise.all(dirPromises);
}

async function extractSymbols(filePath: string): Promise<string[]> {
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
    content = await readFile(filePath, "utf-8");
    // Only read first 10KB for symbol extraction
    if (content.length > 10240) content = content.slice(0, 10240);
  } catch {
    return [];
  }

  const symbols = new Set<string>();
  for (const pattern of patterns) {
    // Reset regex lastIndex
    const re = new RegExp(pattern.source, pattern.flags);
    let match: RegExpExecArray | null = re.exec(content);
    while (match !== null) {
      if (match[1]) symbols.add(match[1]);
      match = re.exec(content);
    }
  }

  return [...symbols];
}
