/**
 * Auto-update version checking and update execution.
 */

const NPM_REGISTRY_URL =
  "https://registry.npmjs.org/@howlerops/iron-rain-cli/latest";
const UPDATE_TIMEOUT_MS = 5000;

export interface UpdateCheckResult {
  currentVersion: string;
  latestVersion: string;
  updateAvailable: boolean;
}

export interface UpdateResult {
  success: boolean;
  previousVersion: string;
  newVersion: string;
  error?: string;
}

export interface VersionInfo {
  package: string;
  version: string;
  bun: string;
  os: string;
  arch: string;
  configPath: string;
}

export interface DoctorCheck {
  name: string;
  status: "ok" | "warn" | "error";
  message: string;
}

/**
 * Compare two semver strings. Returns true if `latest` is newer than `current`.
 */
export function isNewerVersion(current: string, latest: string): boolean {
  const parse = (v: string) => v.replace(/^v/, "").split(".").map(Number);
  const [cMajor, cMinor, cPatch] = parse(current);
  const [lMajor, lMinor, lPatch] = parse(latest);

  if (lMajor !== cMajor) return lMajor > cMajor;
  if (lMinor !== cMinor) return lMinor > cMinor;
  return lPatch > cPatch;
}

/**
 * Check npm registry for latest version.
 */
export async function checkForUpdate(
  currentVersion: string,
): Promise<UpdateCheckResult> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), UPDATE_TIMEOUT_MS);

  try {
    const res = await fetch(NPM_REGISTRY_URL, {
      signal: controller.signal,
      headers: { Accept: "application/json" },
    });

    if (!res.ok) {
      return {
        currentVersion,
        latestVersion: currentVersion,
        updateAvailable: false,
      };
    }

    const data = (await res.json()) as { version: string };
    const latestVersion = data.version;

    return {
      currentVersion,
      latestVersion,
      updateAvailable: isNewerVersion(currentVersion, latestVersion),
    };
  } catch {
    // Network error or timeout — silently fail
    return {
      currentVersion,
      latestVersion: currentVersion,
      updateAvailable: false,
    };
  } finally {
    clearTimeout(timeout);
  }
}

/**
 * Perform the update via bun global install.
 */
export async function performUpdate(): Promise<UpdateResult> {
  const { spawn } = await import("node:child_process");

  // Get current version before update
  const previousVersion = getCurrentVersion();

  return new Promise((resolve) => {
    const child = spawn("bun", ["update", "-g", "@howlerops/iron-rain-cli"], {
      stdio: "pipe",
    });

    let stdout = "";
    let stderr = "";

    child.stdout?.on("data", (d: Buffer) => {
      stdout += d.toString();
    });
    child.stderr?.on("data", (d: Buffer) => {
      stderr += d.toString();
    });

    child.on("close", (code: number | null) => {
      if (code === 0) {
        resolve({
          success: true,
          previousVersion,
          newVersion: "latest", // Will be resolved on next startup
        });
      } else {
        resolve({
          success: false,
          previousVersion,
          newVersion: previousVersion,
          error: stderr || `Process exited with code ${code}`,
        });
      }
    });

    child.on("error", (err: Error) => {
      resolve({
        success: false,
        previousVersion,
        newVersion: previousVersion,
        error: err.message,
      });
    });
  });
}

/**
 * Get the current package version.
 * This is set at startup by the CLI entrypoint via setCurrentVersion().
 * Falls back to the hardcoded value if not set.
 */
let _currentVersion = "0.1.18";

export function getCurrentVersion(): string {
  return _currentVersion;
}

export function setCurrentVersion(version: string): void {
  _currentVersion = version;
}

/**
 * Get full version info for /version command.
 */
export function getVersionInfo(): VersionInfo {
  return {
    package: "@howlerops/iron-rain-cli",
    version: getCurrentVersion(),
    bun: "Bun" in globalThis ? (globalThis as any).Bun.version : "N/A",
    os: `${process.platform} ${process.arch}`,
    arch: process.arch,
    configPath: getConfigPath(),
  };
}

function getConfigPath(): string {
  const home = process.env.HOME || process.env.USERPROFILE || "~";
  return `${home}/.iron-rain/iron-rain.json`;
}

/**
 * Run diagnostic checks for /doctor command.
 */
export async function runDiagnostics(config?: any): Promise<DoctorCheck[]> {
  const checks: DoctorCheck[] = [];

  // 1. Bun runtime check
  const hasBun = "Bun" in globalThis;
  checks.push({
    name: "Bun Runtime",
    status: hasBun ? "ok" : "warn",
    message: hasBun
      ? `v${(globalThis as any).Bun.version}`
      : "Not running in Bun (TUI mode unavailable)",
  });

  // 2. Config file check
  try {
    const { findConfigFile } = await import("../config/loader.js");
    const configPath = findConfigFile();
    checks.push({
      name: "Config File",
      status: configPath ? "ok" : "warn",
      message: configPath
        ? `Found: ${configPath}`
        : "No config file found (using defaults)",
    });
  } catch (e) {
    checks.push({
      name: "Config File",
      status: "error",
      message: `Error checking config: ${e instanceof Error ? e.message : String(e)}`,
    });
  }

  // 3. Data directory check
  const fs = await import("node:fs");
  const home = process.env.HOME || process.env.USERPROFILE || "~";
  const dataDir = `${home}/.iron-rain`;
  const dataDirExists = fs.existsSync(dataDir);
  checks.push({
    name: "Data Directory",
    status: dataDirExists ? "ok" : "warn",
    message: dataDirExists ? dataDir : `${dataDir} (not created yet)`,
  });

  // 4. Environment variable checks
  const envVars = [
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    "GEMINI_API_KEY",
    "OLLAMA_HOST",
  ];

  for (const varName of envVars) {
    const present = !!process.env[varName];
    checks.push({
      name: `ENV: ${varName}`,
      status: present ? "ok" : "warn",
      message: present ? "Set" : "Not set",
    });
  }

  // 5. Bridge availability checks
  if (config?.slots) {
    const { createBridgeForSlot } = await import("../bridge/index.js");
    for (const [slotName, slotConfig] of Object.entries(config.slots) as [
      string,
      any,
    ][]) {
      try {
        const bridge = createBridgeForSlot(slotConfig);
        const available = await bridge.available();
        checks.push({
          name: `Bridge: ${slotName} (${slotConfig.provider})`,
          status: available ? "ok" : "error",
          message: available
            ? `${slotConfig.model} ready`
            : `${slotConfig.model} not available`,
        });
      } catch (e) {
        checks.push({
          name: `Bridge: ${slotName} (${slotConfig.provider})`,
          status: "error",
          message: `Error: ${e instanceof Error ? e.message : String(e)}`,
        });
      }
    }
  }

  return checks;
}
