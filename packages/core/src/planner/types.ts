/**
 * Plan workflow types — PRD-driven task execution.
 */

export type PlanStatus = 'drafting' | 'review' | 'approved' | 'executing' | 'completed' | 'failed' | 'paused';
export type TaskStatus = 'pending' | 'in_progress' | 'completed' | 'failed' | 'skipped';

export interface PlanTask {
  id: string;
  index: number;
  title: string;
  description: string;
  acceptanceCriteria: string[];
  status: TaskStatus;
  targetFiles?: string[];
  dependsOn?: string[];
  result?: {
    output: string;
    filesModified: string[];
    commitHash?: string;
    duration: number;
    tokens: number;
  };
}

export interface Plan {
  id: string;
  title: string;
  description: string;
  prd: string;
  tasks: PlanTask[];
  status: PlanStatus;
  autoCommit: boolean;
  branch?: string;
  createdAt: number;
  updatedAt: number;
  stats: {
    tasksCompleted: number;
    tasksFailed: number;
    totalDuration: number;
    totalTokens: number;
  };
}

// Ralph Wiggum Loop types

export interface LoopConfig {
  want: string;
  completionPromise: string;
  maxIterations: number;
  autoCommit: boolean;
}

export interface LoopIteration {
  index: number;
  action: string;
  result: string;
  completionMet: boolean;
  commitHash?: string;
  duration: number;
  tokens: number;
}

export interface LoopState {
  id: string;
  config: LoopConfig;
  iterations: LoopIteration[];
  status: 'running' | 'completed' | 'failed' | 'paused';
  createdAt: number;
  updatedAt: number;
}

// Callbacks for UI integration

export interface PlanCallbacks {
  onTaskStart?: (task: PlanTask) => void;
  onTaskComplete?: (task: PlanTask) => void;
  onTaskFail?: (task: PlanTask, error: string) => void;
  onPlanComplete?: (plan: Plan) => void;
  onStream?: (chunk: string) => void;
}

export interface LoopCallbacks {
  onIterationStart?: (index: number) => void;
  onIterationComplete?: (iteration: LoopIteration) => void;
  onStream?: (chunk: string) => void;
  onComplete?: (state: LoopState) => void;
}
