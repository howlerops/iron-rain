import type { SlotName } from "../slots/types.js";
import { generateId } from "../utils/id.js";

export interface EpisodeSummary {
  id: string;
  slot: SlotName;
  task: string;
  result: string;
  tokens: number;
  duration: number;
  filesModified?: string[];
  status: "success" | "failure" | "partial";
}

export function createEpisodeSummary(
  partial: Omit<EpisodeSummary, "id">,
): EpisodeSummary {
  return {
    id: generateId(),
    ...partial,
  };
}
