export {
  autoCommit,
  commitIfChanged,
  getChangedFiles,
  getCurrentBranch,
  isGitRepo,
  stashCreate,
  stashPop,
} from "./utils.js";
export { CheckpointManager } from "./checkpoint.js";
export type { Checkpoint } from "./checkpoint.js";
