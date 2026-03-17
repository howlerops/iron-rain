/**
 * @ reference parsing — resolves @file, @dir:, @git: tokens in user input.
 *
 * Syntax:
 *   @./path/to/file.ts  or  @file:path/to/file.ts  — inject file contents
 *   @dir:src/components/                              — inject directory listing
 *   @git:diff | @git:status | @git:log | @git:branch | @git:stash — inject git output
 *   @cortex / @scout / @forge                         — slot routing (word-only, start of input)
 *
 * Disambiguation: @cortex = slot (word-only), @./cortex = file (starts with ./ or ../)
 */

import { exec } from "node:child_process";
import { readdir, readFile, stat } from "node:fs/promises";
import { basename, extname, isAbsolute, relative, resolve } from "node:path";
import { promisify } from "node:util";
import type { SlotName } from "../slots/types.js";

export interface ResolvedReference {
  type: "file" | "directory" | "git" | "image";
  path: string;
  content: string;
  /** Base64-encoded image data (only for type === 'image') */
  imageData?: string;
  /** MIME type (only for type === 'image') */
  mimeType?: string;
}

const IMAGE_EXTENSIONS = new Set([
  ".png",
  ".jpg",
  ".jpeg",
  ".gif",
  ".webp",
  ".bmp",
  ".svg",
]);
const MAX_IMAGE_SIZE = 20 * 1024 * 1024; // 20 MB

const MIME_MAP: Record<string, string> = {
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".bmp": "image/bmp",
  ".svg": "image/svg+xml",
};

export interface ParsedInput {
  targetSlot?: SlotName;
  references: ResolvedReference[];
  prompt: string;
}

const SLOT_ALIASES: Record<string, SlotName> = {
  cortex: "main",
  main: "main",
  scout: "explore",
  explore: "explore",
  forge: "execute",
  execute: "execute",
};

const GIT_WHITELIST = new Set(["diff", "status", "log", "branch", "stash"]);
const MAX_FILE_SIZE = 100 * 1024; // 100 KB

const execAsync = promisify(exec);

/**
 * Parse @ references from user input, resolving files/dirs/git against cwd and contextDirs.
 */
export async function parseReferences(
  input: string,
  cwd: string,
  contextDirs: string[] = [],
): Promise<ParsedInput> {
  const references: ResolvedReference[] = [];
  let targetSlot: SlotName | undefined;
  let prompt = input;

  // 1. Check for slot prefix at the very start: @word followed by space
  const slotMatch = prompt.match(/^@(\w+)\s/);
  if (slotMatch) {
    const alias = slotMatch[1].toLowerCase();
    if (SLOT_ALIASES[alias]) {
      targetSlot = SLOT_ALIASES[alias];
      prompt = prompt.slice(slotMatch[0].length);
    }
  }

  // 2. Extract all @ tokens (file, dir, git, image)
  // Matches: @./path, @../path, @file:path, @dir:path, @git:cmd, @image:path
  const tokenRegex =
    /@(\.\.?\/[^\s]+|file:[^\s]+|dir:[^\s]+|git:[^\s]+|image:[^\s]+)/g;
  const tokens: { match: string; ref: ResolvedReference | null }[] = [];

  let m: RegExpExecArray | null;
  while ((m = tokenRegex.exec(prompt)) !== null) {
    const raw = m[1];
    const ref = await resolveToken(raw, cwd, contextDirs);
    tokens.push({ match: m[0], ref });
  }

  // Strip tokens from prompt and collect resolved references
  for (const t of tokens) {
    prompt = prompt.replace(t.match, "").trim();
    if (t.ref) {
      references.push(t.ref);
    }
  }

  // Clean up extra whitespace
  prompt = prompt.replace(/\s{2,}/g, " ").trim();

  return { targetSlot, references, prompt };
}

async function resolveToken(
  raw: string,
  cwd: string,
  contextDirs: string[],
): Promise<ResolvedReference | null> {
  // @git:cmd
  if (raw.startsWith("git:")) {
    return resolveGit(raw.slice(4), cwd);
  }

  // @dir:path
  if (raw.startsWith("dir:")) {
    return resolveDirectory(raw.slice(4), cwd, contextDirs);
  }

  // @image:path — explicit image reference
  if (raw.startsWith("image:")) {
    return resolveImage(raw.slice(6), cwd, contextDirs);
  }

  // @file:path or @./path or @../path
  const filePath = raw.startsWith("file:") ? raw.slice(5) : raw;

  // Auto-detect images by extension
  const ext = extname(filePath).toLowerCase();
  if (IMAGE_EXTENSIONS.has(ext)) {
    return resolveImage(filePath, cwd, contextDirs);
  }

  return resolveFile(filePath, cwd, contextDirs);
}

async function resolveFile(
  filePath: string,
  cwd: string,
  contextDirs: string[],
): Promise<ResolvedReference | null> {
  const searchPaths = [cwd, ...contextDirs];

  for (const base of searchPaths) {
    const abs = isAbsolute(filePath) ? filePath : resolve(base, filePath);
    try {
      const fileStat = await stat(abs);
      if (!fileStat.isFile()) continue;
      if (fileStat.size > MAX_FILE_SIZE) {
        return {
          type: "file",
          path: abs,
          content: `<file path="${relative(cwd, abs)}">\n(file too large: ${Math.round(fileStat.size / 1024)}KB, limit ${MAX_FILE_SIZE / 1024}KB)\n</file>`,
        };
      }
      const content = await readFile(abs, "utf-8");
      return {
        type: "file",
        path: abs,
        content: `<file path="${relative(cwd, abs)}">\n${content}\n</file>`,
      };
    } catch {
      continue;
    }
  }

  return null;
}

async function resolveDirectory(
  dirPath: string,
  cwd: string,
  contextDirs: string[],
): Promise<ResolvedReference | null> {
  const searchPaths = [cwd, ...contextDirs];

  for (const base of searchPaths) {
    const abs = isAbsolute(dirPath) ? dirPath : resolve(base, dirPath);
    try {
      const entries = await readdir(abs, { withFileTypes: true });
      const lines = entries.map(
        (e) => `${e.isDirectory() ? "d" : "f"} ${e.name}`,
      );
      return {
        type: "directory",
        path: abs,
        content: `<directory path="${relative(cwd, abs) || "."}">\n${lines.join("\n")}\n</directory>`,
      };
    } catch {
      continue;
    }
  }

  return null;
}

async function resolveImage(
  imagePath: string,
  cwd: string,
  contextDirs: string[],
): Promise<ResolvedReference | null> {
  const searchPaths = [cwd, ...contextDirs];

  for (const base of searchPaths) {
    const abs = isAbsolute(imagePath) ? imagePath : resolve(base, imagePath);
    try {
      const fileStat = await stat(abs);
      if (!fileStat.isFile()) continue;

      const ext = extname(abs).toLowerCase();
      const mime = MIME_MAP[ext];
      if (!mime) continue;

      if (fileStat.size > MAX_IMAGE_SIZE) {
        return {
          type: "image",
          path: abs,
          content: `<image path="${relative(cwd, abs)}">\n(image too large: ${Math.round(fileStat.size / 1024 / 1024)}MB, limit ${MAX_IMAGE_SIZE / 1024 / 1024}MB)\n</image>`,
        };
      }

      const data = await readFile(abs);
      const b64 = data.toString("base64");
      const name = basename(abs);
      const sizeKB = Math.round(fileStat.size / 1024);

      return {
        type: "image",
        path: abs,
        content: `<image path="${relative(cwd, abs)}" size="${sizeKB}KB">[Image: ${name}]</image>`,
        imageData: b64,
        mimeType: mime,
      };
    } catch {
      continue;
    }
  }

  return null;
}

async function resolveGit(cmd: string, cwd: string): Promise<ResolvedReference | null> {
  // Only allow whitelisted subcommands
  const subcommand = cmd.split(/[\s-]/)[0];
  if (!GIT_WHITELIST.has(subcommand)) {
    return null;
  }

  try {
    const { stdout } = await execAsync(`git ${cmd}`, {
      cwd,
      timeout: 5000,
      encoding: "utf-8",
      maxBuffer: MAX_FILE_SIZE,
    });
    return {
      type: "git",
      path: `git ${cmd}`,
      content: `<git cmd="${cmd}">\n${stdout.trim()}\n</git>`,
    };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return {
      type: "git",
      path: `git ${cmd}`,
      content: `<git cmd="${cmd}">\n(error: ${message.split("\n")[0]})\n</git>`,
    };
  }
}
