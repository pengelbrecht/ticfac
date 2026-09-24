/**
 * The job-protocol attempt seam's TypeScript types: what one implementer
 * job is asked to be, the records the contract pins for its status and its
 * report, and the four-operation interface every executor implements —
 * start, inspect, collect, cancel (`contracts/job-protocol.json`).
 *
 * These types were born in the Workflow-hosted reconciler
 * (`epic-reconciler.ts`, tick z23) and moved here when tick mn7 deleted that
 * module: the isolate no longer implements a reconciler — an epic's runs are
 * driven by ticfac in the orchestrator container, whose Go executor reaches
 * the per-tick sandbox door (`sandbox-dispatch.ts`) over HTTP — but the
 * executor behind that door (`sandbox-executor.ts`) and the door itself
 * still speak this vocabulary, and so do the tests that hold them to the
 * contract. Deleting the reconciler with the types still in it would have
 * taken live imports with it (xte's finding dedcdb71); keeping them here
 * keeps one home for the seam, importable by whichever module needs it.
 */

/** What the caller asks one implementer job to be. */
export type AttemptSpec = {
  run_id: string;
  epic_id: string;
  tick_id: string;
  /** 1-based, per tick — the number the attempt marker and branch carry. */
  attempt: number;
  role: string;
  project: string;
  /** The branch the attempt commits to; created empty by the executor. */
  write_ref: string;
  base_ref: string;
  title: string;
  /**
   * The model this attempt's worker must run on, when the dispatcher resolved
   * one (tick a08): the sandbox dispatch door carries the profile's model, and
   * a boot that has one outranks the deployment's standing `RUN_WORKER_MODEL`
   * — a choice about this attempt beats a standing one. Absent is the
   * Workflow's own dispatch, which still boots on the per-run ladder.
   */
  model?: string;
};

/** The JobHandle the executor's start returned (SPEC 4.3); opaque here. */
export type AttemptHandle = Record<string, unknown>;

/**
 * The JobStatus contract record (job-protocol $defs.job_status, tick us2):
 * what an executor's `inspect` answers with — the closed state vocabulary the
 * local executors speak, with the `terminal` flag the contract cross-checks
 * against it. `lost` is deliberately NOT terminal: it says the executor can
 * no longer address the handle, which is a statement about the observer and
 * not about the job.
 */
export type AttemptStatus = {
  schema_version: 1;
  job_id: string;
  state: "pending" | "starting" | "running" | "lost" | "succeeded" | "failed" | "cancelled";
  terminal: boolean;
  observed_at: string;
  cursor: string | null;
  observations?: Array<{ at: string; kind: string; detail: string }>;
};

export type AttemptReport = {
  outcome: "done" | "blocked" | "failed";
  commits: number;
  detail: string;
};

/**
 * contracts/job-protocol.json's four operations, the seam every executor is:
 * start, inspect, collect, cancel. The cloud host's concrete executor is the
 * sandbox compatibility executor (`sandbox-executor.ts`); the per-tick
 * sandbox door calls its seams directly, because it needs to know whether a
 * dispatch ADOPTED a live container rather than started one, and a door that
 * cannot boot a container must refuse with the missing pieces in its
 * response body rather than only on the Worker's log.
 */
export interface AttemptExecutor {
  start(spec: AttemptSpec): Promise<AttemptHandle>;
  inspect(handle: AttemptHandle): Promise<AttemptStatus>;
  collect(handle: AttemptHandle): Promise<AttemptReport>;
  cancel(handle: AttemptHandle): Promise<void>;
}
