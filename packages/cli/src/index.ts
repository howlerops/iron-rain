#!/usr/bin/env bun

import { randomBytes } from "node:crypto";
// Read version from package.json
import { createRequire } from "node:module";
import {
  checkForUpdate,
  findConfigFile,
  getVersionInfo,
  loadConfig,
  ModelSlotManager,
  OrchestratorKernel,
  ProviderRegistry,
  performUpdate,
  runDiagnostics,
  setCurrentVersion,
} from "@howlerops/iron-rain";

const _require = createRequire(import.meta.url);
const VERSION: string = _require("../package.json").version;

// Sync version into the core updater so version-check uses the real version
setCurrentVersion(VERSION);

const SPLASH_ART = [
  "  ___                    ____       _       ",
  " |_ _|_ __ ___  _ __   |  _ \\ __ _(_)_ __  ",
  "  | || '__/ _ \\| '_ \\  | |_) / _` | | '_ \\ ",
  "  | || | | (_) | | | | |  _ < (_| | | | | |",
  " |___|_|  \\___/|_| |_| |_| \\_\\__,_|_|_| |_|",
].join("\n");

const TAGLINE = "Multi-model orchestration for terminal-based coding";

function isBunRuntime(): boolean {
  return "Bun" in globalThis;
}

function printSplash(): void {
  console.log(SPLASH_ART);
  console.log(TAGLINE);
  console.log(`v${VERSION}\n`);
}

function printHelp(): void {
  printSplash();
  console.log("Usage:");
  console.log("  iron-rain                    Launch TUI");
  console.log('  iron-rain --headless "task"   Run without TUI');
  console.log("  iron-rain config              Show current config");
  console.log("  iron-rain models              List available models");
  console.log("  iron-rain update              Check for and install updates");
  console.log("  iron-rain doctor              Run diagnostics");
  console.log("  iron-rain version             Show version info");
  console.log("  iron-rain --version           Show version");
  console.log("  iron-rain --help              Show this help");
  if (!isBunRuntime()) {
    console.log(
      "\nNote: TUI mode requires Bun. Install with: bun add -g @howlerops/iron-rain-cli",
    );
  }
}

function printConfig(): void {
  const config = loadConfig();
  console.log(JSON.stringify(config, null, 2));
}

function printModels(): void {
  const registry = new ProviderRegistry();
  for (const provider of registry.list()) {
    console.log(`\n${provider.name}:`);
    for (const model of provider.models) {
      console.log(`  - ${model}`);
    }
  }
}

async function handleUpdate(): Promise<void> {
  console.log("Checking for updates...");
  const result = await checkForUpdate(VERSION);

  if (!result.updateAvailable) {
    console.log(`Already on latest version (${result.currentVersion}).`);
    return;
  }

  console.log(
    `Update available: ${result.currentVersion} -> ${result.latestVersion}`,
  );
  console.log("Installing update...");

  const updateResult = await performUpdate();
  if (updateResult.success) {
    console.log(
      "Update installed successfully. Restart iron-rain to use the new version.",
    );
  } else {
    console.error(`Update failed: ${updateResult.error}`);
    process.exit(1);
  }
}

async function handleDoctor(): Promise<void> {
  console.log("Running diagnostics...\n");

  let config;
  try {
    config = loadConfig();
  } catch {
    config = undefined;
  }

  const checks = await runDiagnostics(config);

  for (const check of checks) {
    const icon =
      check.status === "ok"
        ? "\u2713"
        : check.status === "warn"
          ? "!"
          : "\u2717";
    const color =
      check.status === "ok"
        ? "\x1b[32m"
        : check.status === "warn"
          ? "\x1b[33m"
          : "\x1b[31m";
    console.log(`${color}${icon}\x1b[0m ${check.name}: ${check.message}`);
  }

  const errors = checks.filter((c) => c.status === "error").length;
  const warns = checks.filter((c) => c.status === "warn").length;
  console.log(
    `\n${checks.length} checks: ${checks.length - errors - warns} ok, ${warns} warnings, ${errors} errors`,
  );
}

function handleVersion(): void {
  const info = getVersionInfo();
  console.log(`${info.package} v${info.version}`);
  console.log(`Bun: ${info.bun}`);
  console.log(`OS: ${info.os}`);
  console.log(`Config: ${info.configPath}`);
}

async function runHeadless(prompt: string): Promise<void> {
  const config = loadConfig();
  const slotAssignment = config.slots ?? undefined;
  const slots = new ModelSlotManager(slotAssignment);
  const kernel = new OrchestratorKernel(slots);

  console.log(`Dispatching to main slot: ${slots.getSlot("main").model}\n`);

  const episode = await kernel.dispatch({
    id: randomBytes(16).toString("hex"),
    prompt,
    targetSlot: "main",
  });

  if (episode.status === "failure") {
    console.error(`Error: ${episode.result}`);
    console.error(`\n[${episode.status}] ${episode.duration}ms`);
    process.exit(1);
  }

  console.log(episode.result);
  console.log(
    `\n[${episode.status}] ${episode.tokens} tokens, ${episode.duration}ms`,
  );
}

async function launchTUI(): Promise<void> {
  if (!isBunRuntime()) {
    printSplash();
    console.error(
      "Error: TUI mode requires the Bun runtime (OpenTUI uses bun:ffi for native rendering).",
    );
    console.error("");
    console.error("To use the TUI, install with Bun:");
    console.error("  bun add -g @howlerops/iron-rain-cli");
    console.error("  iron-rain");
    console.error("");
    console.error("Or use headless mode (works with Node.js):");
    console.error('  iron-rain --headless "your prompt here"');
    process.exit(1);
  }

  // Register the OpenTUI solid plugin before importing TUI code.
  // This redirects solid-js from server build (non-reactive) to client
  // build (reactive). Without this, Bun's "node" export condition resolves
  // solid-js to server.js which makes signals static/non-reactive.
  // @ts-ignore — bun-specific runtime plugin API
  const { plugin } = await import("bun");
  // @ts-ignore — bun plugin, no types needed at compile time
  const { default: solidPlugin } = await import("@opentui/solid/bun-plugin");
  plugin(solidPlugin);

  const { startTUI } = await import("@howlerops/iron-rain-tui");
  const config = findConfigFile() ? loadConfig() : undefined;
  await startTUI({ config, version: VERSION });
}

async function main(): Promise<void> {
  const args = process.argv.slice(2);

  if (args.includes("--help") || args.includes("-h")) {
    printHelp();
    return;
  }

  if (args.includes("--version") || args.includes("-v")) {
    console.log(VERSION);
    return;
  }

  if (args[0] === "config") {
    printConfig();
    return;
  }

  if (args[0] === "models") {
    printModels();
    return;
  }

  if (args[0] === "update") {
    await handleUpdate();
    return;
  }

  if (args[0] === "doctor") {
    await handleDoctor();
    return;
  }

  if (args[0] === "version") {
    handleVersion();
    return;
  }

  const headlessIdx = args.indexOf("--headless");
  if (headlessIdx !== -1) {
    const prompt = args[headlessIdx + 1];
    if (!prompt) {
      console.error("Error: --headless requires a prompt argument");
      process.exit(1);
    }
    await runHeadless(prompt);
    return;
  }

  // Default: launch TUI (requires Bun)
  await launchTUI();
}

main().catch((err) => {
  console.error("Fatal error:", err);
  process.exit(1);
});
