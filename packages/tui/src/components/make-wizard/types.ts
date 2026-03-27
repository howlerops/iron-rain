export type MakeWizardStep = "overview" | "tasks" | "options" | "confirm";

export interface MakeWizardOptions {
  autoCommit: boolean;
  maxIterations: number;
  notes: string;
  useLoop: boolean;
}
