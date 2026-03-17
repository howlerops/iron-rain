export {
  checkForUpdate,
  performUpdate,
  isNewerVersion,
  getCurrentVersion,
  getVersionInfo,
  runDiagnostics,
} from './version-check.js';

export type {
  UpdateCheckResult,
  UpdateResult,
  VersionInfo,
  DoctorCheck,
} from './version-check.js';
