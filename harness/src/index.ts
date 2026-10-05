export {
  GATEWAY_MODEL_OVERRIDES,
  GATEWAY_PROVIDER_ID,
  GATEWAY_WORKERS_AI_ROUTE,
  gatewayModelRef,
  gatewayModels,
  type RunToken,
  type WorkersAIGatewayProviderOptions,
  workersAIGatewayProvider,
} from "./gateway/workers-ai.js";
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
export {
  type HostShell,
  type RestoreOutcome,
  restoreWorkspace,
  retireWipSnapshot,
  salvageUncommittedWork,
  type SalvageOutcome,
  WIP_COMMIT_SUBJECT,
  type WipOutcome,
  type WorkspaceCheckpointOptions,
  type WorkspaceGit,
  type WorkspaceReadyOutcome,
  workspaceCheckpointExtension,
} from "./workspace/checkpoints.js";

// ------------------------------------------------- worker contract hooks ---
// Epic 43y step 5 (tick pom): the report linter pushback (#183) and the
// early-exit nudge (060) as onYield follow-ups, and the wall deadline as
// abort().

export {
  armWallDeadline,
  runWorkerReportCheck,
  shellQuote,
  type WallDeadline,
  type WallDeadlineTarget,
  WORKER_HEADLESS_LINE,
  WORKER_MAX_NUDGES,
  WORKER_MAX_PUSHBACKS,
  WORKER_REPORT_CHECKER,
  type WorkerContractOptions,
  workerNudgeMessage,
  workerOnYield,
  workerReportCheckCommand,
} from "./worker-contract.js";

// ------------------------------------------------------- the attempt host ---
// Epic 43y step 6 (tick xd3): one worker attempt driven end to end — the
// container's boot phase, the conversation, the finish phase — the core the
// cloud WorkerAgent Durable Object hosts.

export {
  type AgentEvent,
  type AgentEventStream,
  gatewayModelAccess,
  openDurableObjectStorage,
  watchAttemptEvents,
} from "./host/cloud.js";
export {
  type BootHandoff,
  HARNESS_STATUS_DONE,
  HARNESS_STATUS_UNANSWERED,
  HARNESS_STATUS_WALL,
  PHASE_RETRIES,
  parseBootHandoff,
  pinnedDoor,
  WORKER_BOOT_PROTOCOL,
  WORKER_GIT_IDENTITY,
  WORKER_PROMPT_REQUEST_ID,
  type WorkerAttemptDeps,
  WorkerAttemptHost,
  type WorkerAttemptPhase,
  type WorkerAttemptRecord,
  type WorkerAttemptRecordStore,
  type WorkerAttemptSettlement,
  type WorkerAttemptSpec,
  type WorkerBootProtocol,
} from "./host/worker-attempt.js";
