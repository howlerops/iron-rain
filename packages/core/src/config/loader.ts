import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { parseConfig, type IronRainConfig } from './schema.js';

const CONFIG_FILENAMES = ['iron-rain.json', 'iron-rain.jsonc', '.iron-rainrc.json'];

function stripJsoncComments(text: string): string {
  let result = '';
  let inString = false;
  let escape = false;

  for (let i = 0; i < text.length; i++) {
    const ch = text[i];

    if (escape) {
      result += ch;
      escape = false;
      continue;
    }

    if (inString) {
      if (ch === '\\') escape = true;
      else if (ch === '"') inString = false;
      result += ch;
      continue;
    }

    if (ch === '"') {
      inString = true;
      result += ch;
      continue;
    }

    if (ch === '/' && text[i + 1] === '/') {
      // Skip to end of line
      while (i < text.length && text[i] !== '\n') i++;
      result += '\n';
      continue;
    }

    if (ch === '/' && text[i + 1] === '*') {
      i += 2;
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++;
      i++; // skip closing /
      continue;
    }

    result += ch;
  }

  return result;
}

export function findConfigFile(cwd?: string): string | null {
  const dir = cwd ?? process.cwd();

  for (const name of CONFIG_FILENAMES) {
    const path = resolve(dir, name);
    if (existsSync(path)) return path;
  }

  // Walk up to find project-level config
  const home = process.env.HOME ?? process.env.USERPROFILE ?? '';
  let current = dir;
  while (current !== home && current !== '/') {
    for (const name of CONFIG_FILENAMES) {
      const path = join(current, name);
      if (existsSync(path)) return path;
    }
    current = resolve(current, '..');
  }

  return null;
}

export function writeConfig(config: Partial<IronRainConfig>, cwd?: string): string {
  const dir = cwd ?? process.cwd();
  const configPath = findConfigFile(dir) ?? resolve(dir, 'iron-rain.json');

  const output: Record<string, unknown> = {};
  if (config.slots) output.slots = config.slots;
  if (config.providers) output.providers = config.providers;
  if (config.permission) output.permission = config.permission;
  if (config.agent && config.agent !== 'build') output.agent = config.agent;
  if (config.lcm) output.lcm = config.lcm;
  if (config.theme && config.theme !== 'default') output.theme = config.theme;

  writeFileSync(configPath, JSON.stringify(output, null, 2) + '\n', 'utf-8');
  return configPath;
}

export function loadConfig(cwd?: string): IronRainConfig {
  const configPath = findConfigFile(cwd);

  if (!configPath) {
    return parseConfig({});
  }

  try {
    let raw = readFileSync(configPath, 'utf-8');
    // Strip JSONC comments (only outside of strings)
    if (configPath.endsWith('.jsonc')) {
      raw = stripJsoncComments(raw);
    }
    return parseConfig(JSON.parse(raw));
  } catch (err) {
    throw new Error(
      `Failed to parse config at ${configPath}: ${err instanceof Error ? err.message : String(err)}`,
    );
  }
}
