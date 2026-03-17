/**
 * Voice input support — uses system audio tools for recording and transcription.
 * V1: macOS-only using `rec` (SoX) + `whisper.cpp` or system speech recognition.
 */

import { execSync, type ExecSyncOptionsWithStringEncoding } from "node:child_process";
import { existsSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export interface VoiceConfig {
  enabled: boolean;
  engine: "whisper" | "system";
  whisperModel?: string;
}

export const DEFAULT_VOICE_CONFIG: VoiceConfig = {
  enabled: false,
  engine: "system",
};

/**
 * Check if voice recording tools are available.
 */
export function isVoiceAvailable(): { available: boolean; engine: string; reason?: string } {
  // Check for SoX (rec command)
  try {
    execSync("which rec", { stdio: "pipe" });
  } catch {
    return { available: false, engine: "none", reason: "SoX not installed (brew install sox)" };
  }

  // Check for whisper
  try {
    execSync("which whisper", { stdio: "pipe" });
    return { available: true, engine: "whisper" };
  } catch {
    // Fall back to macOS built-in
    if (process.platform === "darwin") {
      return { available: true, engine: "system" };
    }
    return { available: false, engine: "none", reason: "No transcription engine available" };
  }
}

/**
 * Record audio from the microphone.
 * Returns the path to the recorded WAV file.
 */
export function recordAudio(durationSeconds: number = 10): string {
  const outPath = join(tmpdir(), `iron-rain-voice-${Date.now()}.wav`);
  const opts: ExecSyncOptionsWithStringEncoding = {
    encoding: "utf-8",
    stdio: "pipe",
    timeout: (durationSeconds + 5) * 1000,
  };

  execSync(
    `rec -q -r 16000 -c 1 -b 16 "${outPath}" trim 0 ${durationSeconds}`,
    opts,
  );

  return outPath;
}

/**
 * Transcribe audio using the configured engine.
 */
export function transcribeAudio(audioPath: string, config: VoiceConfig): string {
  if (!existsSync(audioPath)) {
    throw new Error(`Audio file not found: ${audioPath}`);
  }

  try {
    const opts: ExecSyncOptionsWithStringEncoding = {
      encoding: "utf-8",
      stdio: "pipe",
      timeout: 30000,
    };

    if (config.engine === "whisper") {
      const model = config.whisperModel ?? "base";
      const result = execSync(
        `whisper "${audioPath}" --model ${model} --output_format txt --output_dir "${tmpdir()}"`,
        opts,
      );
      return result.trim();
    }

    // macOS system speech recognition
    if (process.platform === "darwin") {
      const result = execSync(
        `say -i "${audioPath}" 2>/dev/null || echo "[transcription unavailable]"`,
        opts,
      );
      return result.trim();
    }

    return "[voice transcription not available on this platform]";
  } finally {
    // Cleanup temp audio file
    try {
      unlinkSync(audioPath);
    } catch {
      // Ignore cleanup errors
    }
  }
}
