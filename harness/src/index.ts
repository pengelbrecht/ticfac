export {
  type ConformanceCaseResult,
  runStorageConformanceCase,
  storageConformanceCaseNames,
} from "./storage/conformance.js";
export {
  DurableObjectSqliteDatabase,
  type DurableObjectSqliteTarget,
} from "./storage/durable-object-sqlite.js";
export { storageConformanceAssertions } from "./testing/assertions.js";

// ------------------------------------------------- execution environments ---
// Epic 43y step 3 (tick kgk): the envs a worker's tools run through.

export {
  boundaryGuardShim,
  defaultGuardDir,
  type GuardInstallBase,
  guardPathPrefix,
  installBoundaryGuard,
  WORKER_TK_DENIED,
} from "./env/boundary-guard.js";
export {
  BASH_NONCE_VAR,
  BASH_POLL_MS,
  bashNonceMarker,
  FactorySandboxEnv,
  type FactorySandboxEnvOptions,
  SandboxUnavailableError,
} from "./env/factory-sandbox.js";
export {
  PROCESS_CWD,
  type SandboxBootOptions,
  type SandboxDoor,
  type SandboxOutput,
  type SandboxProcessState,
  type SandboxProcessView,
  type SandboxRunOptions,
  type SandboxRunOutcome,
} from "./env/sandbox-door.js";
export { createTrackedBashTool } from "./tools/tracked-bash.js";
