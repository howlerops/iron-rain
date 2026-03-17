export type {
  DoctorCheck,
  UpdateCheckResult,
  UpdateResult,
  VersionInfo,
} from "./version-check.js";
export {
  checkForUpdate,
  getCurrentVersion,
  getVersionInfo,
  isNewerVersion,
  performUpdate,
  runDiagnostics,
} from "./version-check.js";
