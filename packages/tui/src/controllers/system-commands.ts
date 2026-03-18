import {
  checkForUpdate,
  getVersionInfo,
  loadConfig,
  performUpdate,
  runDiagnostics,
} from "@howlerops/iron-rain";
import type { AddSystemMessage } from "./context.js";

interface SystemCommandOptions {
  text: string;
  addSystemMessage: AddSystemMessage;
  version?: string;
}

export async function handleSystemCommand({
  text,
  addSystemMessage,
  version,
}: SystemCommandOptions): Promise<boolean> {
  if (text === "/version") {
    const info = getVersionInfo();
    addSystemMessage(
      `**${info.package}** v${info.version}\n` +
        `Bun: ${info.bun}\n` +
        `OS: ${info.os}\n` +
        `Config: ${info.configPath}`,
    );
    return true;
  }

  if (text === "/update") {
    addSystemMessage("Checking for updates...");
    try {
      const result = await checkForUpdate(version ?? "0.1.6");
      if (result.updateAvailable) {
        addSystemMessage(
          `Update available: ${result.currentVersion} -> ${result.latestVersion}\nInstalling...`,
        );
        const updateResult = await performUpdate();
        if (updateResult.success) {
          addSystemMessage("Update installed successfully. Restarting...");
          setTimeout(() => process.exit(0), 500);
        } else {
          addSystemMessage(`Update failed: ${updateResult.error}`);
        }
      } else {
        addSystemMessage(
          `Already on latest version (${result.currentVersion}).`,
        );
      }
    } catch (err) {
      addSystemMessage(
        `Update check failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return true;
  }

  if (text === "/doctor") {
    addSystemMessage("Running diagnostics...");
    try {
      const config = loadConfig();
      const checks = await runDiagnostics(config);
      const lines = checks.map((c) => {
        const icon =
          c.status === "ok" ? "\u2713" : c.status === "warn" ? "!" : "\u2717";
        return `${icon} **${c.name}**: ${c.message}`;
      });
      addSystemMessage(`## Diagnostics\n${lines.join("\n")}`);
    } catch (err) {
      addSystemMessage(
        `Diagnostics failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return true;
  }

  return false;
}
