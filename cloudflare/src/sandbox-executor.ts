/**
 * The sandbox compatibility executor (SPEC §12 Phase 4 item 4, tick k4s):
 * the cloud host's concrete `AttemptExecutor`, built over the
 * `SandboxBinding` seam `sandbox.ts` already declares.
 *
 * WHO CALLS IT NOW. This module was built as the Workflow-hosted
 * reconciler's executor (tick z23, `epic-reconciler.ts`); tick mn7 deleted
 * that reconciler — an epic's runs are driven by ticfac in the orchestrator
 * container, whose Go executor (`internal/exec/cloudflaresandbox`)
 * cannot create a sibling Sandbox itself, because the binding is a Worker
 * binding. It reaches this module's machinery over HTTP instead, through
 * the per-tick sandbox door (`sandbox-dispatch.ts`): the door calls
 * `startNamedAttempt` and `namedAttemptStatus` directly — it needs to KNOW
 * whether a dispatch ADOPTED a live container, which the closed four-op
 * interface cannot say — and `sandboxExecutorDepsFromEnv` is the wiring
 * both the door and `sandboxExecutorFromEnv` share. The four-operation
 * interface below stays: it is the contract's own shape
 * (`contracts/job-protocol.json`'s start / inspect / collect / cancel, the
 * same seam every local executor implements), its tests are what hold the
 * handle/status/report records to the pinned schemas, and it is what a
 * future door route that needs inspect or cancel over HTTP would build
 * on. `worker-boot.ts` composes each boot and `worker-dispatch.ts`'s
 * `spawnWorker` runs the green-start trap and the confirmed-dispatch wait
 * against a fresh container named per ATTEMPT.
 *
 * THE HANDLE IS THE CONTRACT'S (tick us2). What `start` returns is a
 * job-protocol `JobHandle` — the closed top level (schema_version, job_id,
 * attempt, executor, issued_at) and the one open `handle` object the contract
 * reserves for executor-private addressing — validated against the pinned
 * `$defs.job_handle` by this module's tests, so a cloud-produced handle is
 * indistinguishable from a local one to everything that reads records. The
 * in-container caller stores it in its dispatch marker's `handle` slot, the
 * same slot the run-state contract's own golden attempt record uses for
 * herdr's addressing. It deliberately carries NO credential: the handle is what the
 * run's marker stores on the run branch, and a secret in a committed record
 * is a leak by construction. The boot inputs are re-derived from the seam at
 * every use, each boot minting a fresh per-worker gateway credential that
 * revokes nothing (tick 53s): a run holds SEVERAL workers at once, so one
 * worker's boot must never cost a sibling its token the way an
 * orchestrator's rotating boot would.
 *
 * THE ATTEMPT'S REF CARRIES THE WORK (tick us2, finding 1d778f96). The
 * container lands its push on a per-attempt branch (`attemptLandingBranch`,
 * worker-boot.ts — the image derives the branch from the boot's two slots,
 * and the attempt rides the epic slot), and the executor makes the attempt's
 * own push at collect: the landing branch's head goes onto the attempt's
 * write_ref through the `GitRefWriter` seam (`git-refs.ts`), the same push
 * the local executor makes itself (pushBranch), and the collect then reads
 * the WRITE_REF — so the marker, the collect and the settle's integrate all
 * name one ref, and a redispatch's fresh attempt cannot count the previous
 * attempt's commits.
 */

import { workerLogSink, writeWorkerLogSegment } from "./artifacts";
import type {
  AttemptExecutor,
  AttemptHandle,
  AttemptReport,
  AttemptSpec,
  AttemptStatus,
} from "./attempt-protocol";
import { containerGitToken, planSandboxGit } from "./credentials";
import {
  putSandboxJobLogCursor,
  recordSandboxAttemptBoot,
  recordSandboxJobSettled,
  type SandboxAttemptBoot,
  sandboxAttemptBootModel,
  sandboxJobLogCursor,
  sandboxJobSettled,
} from "./db";
import { factoryBaseURL, issueWorkerRunToken, runGatewayEndpoint } from "./gateway";
import { type GitRefWriter, gitRefWriter, writeRefBranch } from "./git-refs";
import { containerGitHub } from "./github-app";
import type { Env } from "./index";
import {
  deploymentImage,
  type OrchestratorSandbox,
  repoURL,
  type SandboxBinding,
  type SandboxProcessState,
  sandboxBinding,
} from "./sandbox";
import {
  type WorkerAgentHosting,
  type WorkerAgentResolver,
  type WorkerAgentStub,
  workerAgentsFromEnv,
} from "./worker-agent";
import {
  attemptLandingBranch,
  WORKER_COMMAND,
  type WorkerBootInput,
  workerBootEnv,
  workerHarness,
  workerHarnessBudgetMs,
  workerModel,
  workerWorkSpec,
} from "./worker-boot";
import {
  needsHuman,
  type WorkerCollector,
  type WorkerReport,
  workerCollector,
} from "./worker-collect";
import {
  type SalvageSpec,
  type SpawnOptions,
  salvageWorker,
  spawnWorker,
  teardownWorker,
} from "./worker-dispatch";

/**
 * A deployment variable as a string-or-null, matching `run-workflow.ts`'s
 * `textVar`: an exported empty string is a defeated default, not an unset one.
 */
function textVar(env: Env, name: "RUN_WORKER_HARNESS" | "RUN_WORKER_MODEL"): string | null {
  const raw = env[name];
  return typeof raw === "string" && raw.trim() !== "" ? raw.trim() : null;
}

// --------------------------------------------------------------- the name ---

/** The executor name a dispatch rides, and the contract's enum spells. */
export const SANDBOX_EXECUTOR_NAME = "cloudflare-sandbox";

/** The job_handle contract's own schema_version (job-protocol $defs.job_handle). */
export const JOB_HANDLE_SCHEMA_VERSION = 1;

/**
 * The container one attempt's worker runs in, addressed by name.
 *
 * The attempt is in the name on purpose, for the same reason
 * `sandboxName` puts it there for the orchestrator: a redispatch after a
 * spent attempt must land in a FRESH container — the previous one is
 * expected to be broken, and reusing its name is how you inherit whatever
 * broke it. The tick is in the name too, because unlike the orchestrator a
 * run may hold SEVERAL workers at once and each needs its own container.
 */
export function attemptSandboxName(
  runID: string,
  tickID: string,
  attempt: number,
  jobID?: string,
): string {
  return attemptSandboxNameForSlot(
    runID,
    tickID,
    attempt,
    attemptJobSlot(runID, tickID, attempt, jobID) ?? "",
  );
}

/**
 * The same name from a job's recorded SLOT ('' for the attempt's own job) —
 * how the boot records (migration 0018) name the container they booted, which
 * is what the reclaim (container-capacity.ts) addresses.
 */
export function attemptSandboxNameForSlot(
  runID: string,
  tickID: string,
  attempt: number,
  slot: string,
): string {
  return slot === "" ? `${runID}-${tickID}-${attempt}` : `${runID}-${tickID}-${attempt}-${slot}`;
}

/**
 * The job-protocol job_id of one attempt: `run-<run>/tick-<tick>/attempt-<n>`,
 * in ONE place because two records must agree about it — the handle `start`
 * returns and the status an identity-based lookup answers with. A client
 * keying on job_id must find the same value whether it asked to start the
 * attempt or to read it back, and a spelling written twice is how the two
 * halves of that guarantee drift apart.
 */
export function attemptJobID(runID: string, tickID: string, attempt: number): string {
  return `run-${runID}/tick-${tickID}/attempt-${attempt}`;
}

/**
 * The job id a spec names: the caller's own when it stated one, else the
 * attempt's (`attemptJobID`). Every answer the door gives about a job —
 * the handle start mints, the status the state route reads — carries THIS,
 * so a caller keying on job_id finds its own job and never another's.
 */
export function specJobID(spec: {
  run_id: string;
  tick_id: string;
  attempt: number;
  job_id?: string;
}): string {
  return spec.job_id ?? attemptJobID(spec.run_id, spec.tick_id, spec.attempt);
}

/**
 * The slot a role job that is NOT the attempt itself adds to the attempt's
 * container name, landing branch and boot record — or undefined for the
 * attempt's own job (no job id, or exactly `attemptJobID`), whose names stay
 * what they always were so a container an earlier deployment booted is
 * still the one this identity re-addresses.
 *
 * WHY (the hn6 cloud-run stall). The reconciler runs several jobs under ONE
 * attempt number: the implement attempt `…/attempt-1`, then — when its
 * integrated gate fails — the repair `…/repair-1` and its retries
 * `…/repair-1-r2`, `…/repair-1-r3`, all with attempt 1. Keyed by
 * (run, tick, attempt) alone, every repair re-addressed the implement
 * attempt's settled container, the door answered "succeeded", and the Go
 * executor refused each repair as a settled attempt started again — the
 * repair bound was spent without a repair ever booting. A job's identity is
 * its FULL job id (herdr's rule since PR #88), so a different job id is a
 * different container, a different landing branch and a different record.
 *
 * The slot is the job id's own tail (the run prefix and, when present, the
 * tick's, stripped), reduced to a container-name- and ref-safe alphabet for
 * a person reading it, plus a hash of the WHOLE job id — so two job ids that
 * reduce to one readable tail still name two containers.
 */
export function attemptJobSlot(
  runID: string,
  tickID: string,
  attempt: number,
  jobID?: string,
): string | undefined {
  if (jobID === undefined || jobID === attemptJobID(runID, tickID, attempt)) return undefined;
  let tail = jobID;
  const runPrefix = `run-${runID}/`;
  if (tail.startsWith(runPrefix)) tail = tail.slice(runPrefix.length);
  const tickPrefix = `tick-${tickID}/`;
  if (tail.startsWith(tickPrefix)) tail = tail.slice(tickPrefix.length);
  // The readable part gives way first: the container's name is
  // `<run>-<tick>-<attempt>-<slot>`, and the SDK refuses a sandbox id longer
  // than SANDBOX_ID_MAX before it boots anything (hn6 run_ee8e: both retries
  // of 378's resolve job, `…/resolve-4-r2` and `-r3`, came to 64 characters
  // and were refused in seconds, spending the tick's resolve allowance
  // without a job running). A slot that already fitted keeps its spelling,
  // so a container booted under it is still re-addressed by it; the hash of
  // the whole job id is what keeps a shortened slot distinct.
  const digest = fnv1a32(jobID);
  const room = SANDBOX_ID_MAX - `${runID}-${tickID}-${attempt}-`.length - `-${digest}`.length;
  const readable = tail
    .replace(/[^A-Za-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, Math.max(0, Math.min(40, room)))
    .replace(/-+$/, "");
  if (readable === "") return room >= "job".length ? `job-${digest}` : digest;
  return `${readable}-${digest}`;
}

/**
 * The longest sandbox id the Cloudflare Sandbox SDK accepts
 * (`sanitizeSandboxId`: "Sandbox ID must be 1-63 characters long." — a DNS
 * label's bound).
 */
export const SANDBOX_ID_MAX = 63;

/**
 * FNV-1a over the UTF-16 code units, as 8 hex digits: a synchronous,
 * deterministic distinguisher for {@link attemptJobSlot}, not a security
 * primitive — the run's credential, not the name, is what stops one run
 * addressing another's containers.
 */
function fnv1a32(text: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}

// -------------------------------------------------------------- the handle ---

/**
 * This executor's private addressing, inside the contract's one open `handle`
 * object ("a Cloudflare workspace id and a local pid have nothing in common").
 * No credential, ever — see the module header.
 */
export type SandboxHandlePayload = {
  /** The container's name — the identity a later leg re-addresses it by. */
  sandbox: string;
  /** The work process inside that container, once the dispatch was confirmed. */
  process_id: string | null;
  /** The epic base the container clones at and collect compares against. */
  base_sha: string;
  /** The per-attempt branch the container pushes — collect's landing zone. */
  branch: string;
  /** The attempt's write_ref — the ref collect puts the work on and reads. */
  write_ref: string;
  /** What `start` saw: the container is launched, or why it is not. */
  launched: boolean;
  detail: string;
  /** The dispatch's remaining inputs, so `cancel` can re-derive a boot. */
  run_id: string;
  epic_id: string;
  tick_id: string;
  role: string;
  project: string;
  base_ref: string;
  title: string;
  /**
   * The model the container was booted with — its `TICKS_MODEL`, read off
   * the boot environment rather than restated (tick a08), so the record a
   * caller keeps names the model that actually ran. For an ADOPTION (tick dyo)
   * that is the recorded model of the boot that started the RUNNING work
   * process — never an echo of the model the adopting request carried.
   */
  model: string;
  /**
   * The harness the container was booted with — its `TICKS_HARNESS`, read
   * off the boot environment rather than restated (tick 9iz), so the record
   * a caller keeps names the harness that actually ran, never the
   * deployment's own agreeing with it by luck.
   */
  harness: string;
};

/**
 * The JobHandle this executor's `start` returns — the pinned contract's
 * shape (job-protocol $defs.job_handle): a closed top level of identity and
 * executor name, the issue time, and the one open `handle` object. The same
 * record a local executor returns, so a reader of one host's handles cannot
 * tell which host made it.
 */
export type SandboxJobHandle = {
  schema_version: typeof JOB_HANDLE_SCHEMA_VERSION;
  job_id: string;
  attempt: number;
  executor: typeof SANDBOX_EXECUTOR_NAME;
  handle: SandboxHandlePayload;
  issued_at: string;
};

/**
 * Reads the JobHandle an operation is asked to re-address by.
 *
 * Two legitimate shapes reach here: the handle `start` returned (the
 * protocol's own input), and a dispatch marker's `job_handle` carrying it
 * nested under `handle` — what the reconciler re-reads from origin. Both are
 * decoded strictly, and a record that is neither — a marker written before
 * the handle was persisted, say — is a loud refusal rather than a cast to
 * undefined: "I have no id for it" must never be allowed to read as
 * "nothing is running here", and a marker that names no container is a
 * marker nobody can re-address.
 */
function jobHandleOf(record: AttemptHandle): SandboxJobHandle {
  const nested = (record as { handle?: unknown }).handle;
  const candidate = isJobHandle(nested) ? nested : record;
  if (!isJobHandle(candidate)) {
    const named = (candidate as { executor?: unknown }).executor;
    if (typeof named === "string" && named !== SANDBOX_EXECUTOR_NAME) {
      throw new Error(
        `the record names executor ${named}; this is the ${SANDBOX_EXECUTOR_NAME} executor`,
      );
    }
    throw new Error(
      "this attempt record carries no executor handle: without it nothing can re-address " +
        "the container this attempt ran in — the marker proves the dispatch, the handle " +
        "proves the job",
    );
  }
  return candidate;
}

/** Whether a record is this executor's contract JobHandle. */
function isJobHandle(value: unknown): value is SandboxJobHandle {
  if (typeof value !== "object" || value === null) return false;
  const record = value as Record<string, unknown>;
  return (
    record.schema_version === JOB_HANDLE_SCHEMA_VERSION &&
    typeof record.job_id === "string" &&
    record.job_id !== "" &&
    record.executor === SANDBOX_EXECUTOR_NAME &&
    typeof record.handle === "object" &&
    record.handle !== null
  );
}

// --------------------------------------------------------------- the seams ---

/** How one dispatch's boot inputs are composed: the caller holds the run. */
export type SandboxBootSeam = (spec: AttemptSpec) => Promise<WorkerBootInput>;

/**
 * The durable record of what one attempt's container was booted on (tick dyo).
 *
 * An ADOPTION answers for a work process an earlier dispatch started, whose
 * model THIS request never chose — a config edited between incarnations, a
 * deployment redeployed mid-run — and the handle's `model` field must name
 * the model the RUNNING container is on, never the one the new request
 * carries, because the caller's record and its model checks read exactly
 * that field. The process list cannot read a live process's environment, so
 * the boot is RECORDED here, before the container is addressed, and an
 * adoption READS it back: the door is the only party that boots under an
 * identity, so its own durable record of what it commanded is what the
 * container is on.
 */
export type SandboxBootRecord = {
  /** Records the model one attempt's container boots on, keyed by identity. */
  record(boot: SandboxAttemptBoot): Promise<void>;
  /**
   * Reads the model the named attempt's container was booted on, or null
   * when no boot was recorded — a container an older deployment booted,
   * which the adoption refuses on rather than guess about.
   */
  modelOf(identity: {
    run_id: string;
    tick_id: string;
    attempt: number;
    job_id?: string;
  }): Promise<string | null>;
};

/**
 * The adoption that cannot tell the truth: a live work process exists under
 * the identity, and no recorded boot says which model it is on. The door
 * refuses the start rather than naming a model it cannot state — the caller
 * holds the attempt for a person instead of recording a guess.
 */
export class AdoptionModelUnknownError extends Error {
  constructor(identity: { run_id: string; tick_id: string; attempt: number; job_id?: string }) {
    super(
      `the running container for ${specJobID(identity)} ` +
        "has no recorded boot, so the model it is on cannot be stated: it was booted by a deployment " +
        "that did not record boots, and adopting it would name a model nobody observed",
    );
    this.name = "AdoptionModelUnknownError";
  }
}

/**
 * The SDK's refusal of a container NAME (`sanitizeSandboxId`: longer than 63
 * characters, a leading or trailing hyphen, a reserved name), as the SDK's
 * own reason — or null for any other error. The same name is refused on
 * every ask, so this is a permanent answer, never a fault to retry (hn6
 * run_ee8e: two resolve retries were refused this way in seconds, each read
 * as a door fault worth asking again, until the tick's allowance was spent).
 */
export function sandboxNameRejection(error: unknown): string | null {
  if (!(error instanceof Error) || error.name !== "SandboxSecurityError") return null;
  const code = (error as { code?: unknown }).code;
  if (typeof code !== "string" || !code.includes("SANDBOX_ID")) return null;
  return error.message;
}

/** A start whose container name the SDK refused: the name, and the SDK's reason. */
export class InvalidSandboxNameError extends Error {
  constructor(
    readonly sandbox: string,
    readonly reason: string,
  ) {
    super(
      `the container name ${sandbox} is one the Sandbox SDK refuses (${reason}); the name is derived ` +
        "from the job's identity, so asking again under the same identity is refused the same way",
    );
    this.name = "InvalidSandboxNameError";
  }
}

/**
 * What the executor is built from. Every dependency is a seam for the same
 * reason `SANDBOXES` is one: the four operations' mapping is the thing worth
 * testing, and a lifecycle exercisable only by starting a real container is
 * a lifecycle nobody tests.
 */
export type SandboxExecutorDeps = {
  binding: SandboxBinding;
  /** The durable-layer reader collect goes through — never a sandbox reference. */
  collector: WorkerCollector;
  /** The git writer that puts the work on the attempt's write_ref (tick us2). */
  refs: GitRefWriter;
  /** Boot inputs for one dispatch: repo URL, gateway credential, base SHA, image. */
  boot: SandboxBootSeam;
  /**
   * The durable record of what each attempt's container was booted on
   * (tick dyo): written before the container is addressed, read by an
   * adoption so its handle names the RUNNING container's model.
   */
  boots: SandboxBootRecord;
  /** Spawn knobs (sleep, log sinks, budgets) — the wave machinery's own. */
  spawn?: SpawnOptions;
  /**
   * Where a job's log cursor starts (tick 86y): the confirm window copied the
   * worker's first output, and the state route copies the rest from where
   * it ended. Absent means nothing continues the stream.
   */
  jobLogs?: SandboxJobLogs;
  /**
   * Which runs' attempts are hosted by a WorkerAgent (epic 43y, tick xd3,
   * worker-agent.ts): asked per run, null for a run whose workers stay the
   * container's own all-in-one process. Absent on a deployment that binds no
   * WORKER_AGENTS.
   */
  agents?: WorkerAgentResolver;
};

// ------------------------------------------------------------ job logs ---

/**
 * A job's worker output, copied into the run's log stream past the confirm
 * window (tick 86y). The door's spawn drained a worker's output only while
 * it confirmed the dispatch — which ends at the first byte — so a worker's
 * log stopped at "checked out <sha>" however long it ran, and epic hn6's r5i
 * try 2 died five minutes later without a word anyone could read. The state
 * route now copies what the work process printed since the last look, on
 * every look, and drains it to the end before a settled container is
 * reclaimed (namedAttemptStatus): the reclaim destroys the container, and
 * the output with it.
 */
export type SandboxJobLogs = {
  /** A fresh boot's dispatch is confirmed: its output up to `offset` is already copied. */
  started(identity: SandboxJobIdentity, processID: string, offset: number): Promise<void>;
  /** Copies what the work process printed since the job's cursor. */
  drain(
    identity: SandboxJobIdentity,
    sandbox: Pick<OrchestratorSandbox, "readOutput">,
    processID: string,
  ): Promise<void>;
};

/** The job log cursors over D1 (migration 0020), the segments into the run's R2 stream. */
export function d1JobLogs(db: D1Database, bucket: R2Bucket, project: string): SandboxJobLogs {
  const key = (identity: SandboxJobIdentity) => ({
    run_id: identity.run_id,
    tick_id: identity.tick_id,
    attempt: identity.attempt,
    job: attemptJobSlot(identity.run_id, identity.tick_id, identity.attempt, identity.job_id) ?? "",
  });
  return {
    async started(identity, processID, offset) {
      // Its own epoch, taken now: the confirm window's segments were written
      // under an earlier one, so the continuation sorts after them.
      await putSandboxJobLogCursor(db, key(identity), {
        process_id: processID,
        offset,
        epoch: Date.now(),
        seq: 0,
      });
    },
    async drain(identity, sandbox, processID) {
      const found = await sandboxJobLogCursor(db, key(identity));
      // No cursor for this process: a boot from before the cursor existed,
      // or a fresh boot under a reused identity. Its whole output is copied
      // from the start — the confirm window's first lines twice at worst,
      // never a hole.
      const cursor =
        found !== null && found.process_id === processID
          ? found
          : { process_id: processID, offset: 0, epoch: Date.now(), seq: 0 };
      const chunk = await sandbox.readOutput(processID, cursor.offset);
      if (chunk.text === "" && chunk.offset === cursor.offset && found !== null) return;
      let seq = cursor.seq;
      if (chunk.text !== "") {
        seq += 1;
        await writeWorkerLogSegment(
          bucket,
          project,
          identity.run_id,
          identity.tick_id,
          cursor.epoch,
          seq,
          chunk.text,
        );
      }
      await putSandboxJobLogCursor(db, key(identity), {
        process_id: processID,
        offset: chunk.offset,
        epoch: cursor.epoch,
        seq,
      });
    },
  };
}

/** A drain that fails is observed, never the status read's failure. */
async function drainJobLog(
  logs: SandboxJobLogs | undefined,
  identity: SandboxJobIdentity,
  sandbox: Pick<OrchestratorSandbox, "readOutput">,
  processID: string,
): Promise<void> {
  if (logs === undefined) return;
  try {
    await logs.drain(identity, sandbox, processID);
  } catch (error) {
    console.error(
      `factory sandbox door: could not copy ${identity.tick_id} attempt ${identity.attempt}'s ` +
        `worker output: ${String(error)}`,
    );
  }
}

// ----------------------------------------------------------------- start ---

/**
 * The sandbox an adoption question is answered against: the named container,
 * re-addressed on every look. A Workflow replay or a restarted pass cannot
 * carry a live `OrchestratorSandbox` object across the step boundary, so
 * nothing here does.
 */
async function namedSandbox(binding: SandboxBinding, name: string): Promise<OrchestratorSandbox> {
  return binding.get(name);
}

/**
 * Whether one process view is this attempt's WORK process.
 *
 * By command, not by id: an adoption question is asked exactly when the id
 * is the thing that may be stale — a supervisor that died between starting
 * the work process and recording its id holds no id to ask about, and "I
 * have no id for it" must never read as "nothing is running here".
 */
function isWorkProcess(view: { command?: string }): boolean {
  return view.command === WORKER_COMMAND;
}

/**
 * Finds this container's live work process, if it has one.
 *
 * `listProcesses`, never `getProcess` alone — the evidence gap the seam
 * documents (src/sandbox.ts) is what this closes: a dispatched container
 * whose process id the caller lost is still found, still running, still
 * ADOPTED rather than dispatched over.
 */
async function findWorkProcess(sandbox: OrchestratorSandbox): Promise<{ id: string } | null> {
  const listed = await sandbox.listProcesses();
  for (const view of listed ?? []) {
    if (isWorkProcess(view) && view.state === "running") {
      return { id: view.id };
    }
  }
  return null;
}

/**
 * Starts one attempt's worker: a fresh container, the green-start probe, and
 * a confirmed dispatch — or, when the named container already holds a live
 * work process, the SAME handle back. Adoption, not a second dispatch: a
 * Workflow step replay must never boot a second worker over a live one,
 * which is the local executor's "never redispatch a live attempt" rule
 * restated for a substrate whose attempts cost real containers.
 *
 * Exported (tick 8ty) because this is no longer only the reconciler's in-isolate
 * operation: the HTTP dispatch door (`sandbox-dispatch.ts`) is a second caller
 * that needs the same adoption decision — and needs to KNOW which of the two
 * happened, because "this attempt is already running" is a fact its caller
 * records and must not be guessed back out of the handle's detail text.
 */
export async function startNamedAttempt(
  deps: SandboxExecutorDeps,
  spec: AttemptSpec,
): Promise<{ handle: SandboxJobHandle; adopted: boolean }> {
  try {
    return await startNamedAttemptUnchecked(deps, spec);
  } catch (error) {
    const reason = sandboxNameRejection(error);
    if (reason === null) throw error;
    throw new InvalidSandboxNameError(
      attemptSandboxName(spec.run_id, spec.tick_id, spec.attempt, spec.job_id),
      reason,
    );
  }
}

async function startNamedAttemptUnchecked(
  deps: SandboxExecutorDeps,
  spec: AttemptSpec,
): Promise<{ handle: SandboxJobHandle; adopted: boolean }> {
  // Named by the FULL job id: a repair of attempt 1 is not attempt 1, and a
  // settled attempt's container must never answer for the job that repairs
  // it (attemptJobSlot says why).
  const jobID = specJobID(spec);
  const slot = attemptJobSlot(spec.run_id, spec.tick_id, spec.attempt, spec.job_id);
  const name = attemptSandboxName(spec.run_id, spec.tick_id, spec.attempt, spec.job_id);
  // The per-attempt landing branch the container derives from the boot
  // (worker-boot.ts puts the attempt — and a role job's slot — in the epic
  // slot): the work the container pushes lands on no other job's branch, so
  // a redispatch starts from the base it was given, not from the previous
  // attempt's work, and a repair never adopts the attempt's pushed branch.
  const landing = attemptLandingBranch(spec.epic_id, spec.attempt, spec.tick_id, slot);
  const payload: Omit<
    SandboxHandlePayload,
    "process_id" | "launched" | "detail" | "model" | "harness"
  > = {
    sandbox: name,
    base_sha: "",
    branch: landing,
    write_ref: spec.write_ref,
    run_id: spec.run_id,
    epic_id: spec.epic_id,
    tick_id: spec.tick_id,
    role: spec.role,
    project: spec.project,
    base_ref: spec.base_ref,
    title: spec.title,
  };

  // A run whose workers are WorkerAgents (epic 43y, tick xd3): the attempt
  // is the agent's, addressed by the same name, and the container is only
  // where its tools run — asked nothing here.
  const hosting = deps.agents === undefined ? null : await deps.agents(spec.run_id);
  if (hosting !== null) {
    return startHostedAttempt(deps, spec, hosting, { jobID, slot, name, payload });
  }

  // Adoption first: a container already holding a live work process is this
  // attempt's, by the name nobody else would boot under, and starting a
  // second one beside it is how a run pays twice for one tick.
  //
  // The model the handle names on this path is the RUNNING container's, read
  // from the record of the boot that started it (tick dyo) — never the model
  // THIS request carries. Until that distinction was made, an adoption named
  // the new dispatch's model over a container some other dispatch booted, and
  // the caller's model check — the one that holds a container to the model its
  // dispatch resolved, and with it the Workers-AI-only rule — could never
  // fire here: the door was echoing the question back as the answer.
  const sandbox = await namedSandbox(deps.binding, name);
  const running = await findWorkProcess(sandbox);
  if (running !== null) {
    const runningModel = await deps.boots.modelOf(spec);
    if (runningModel === null || runningModel.trim() === "") {
      // No recorded boot for a live work process: a container an older
      // deployment booted. Adopting it would put an unstated model in a
      // record every trace reads, so the start is refused and the caller
      // holds the attempt — never a guess in a handle.
      throw new AdoptionModelUnknownError(spec);
    }
    const boot = await deps.boot(spec);
    return {
      handle: {
        schema_version: JOB_HANDLE_SCHEMA_VERSION,
        job_id: jobID,
        attempt: spec.attempt,
        executor: SANDBOX_EXECUTOR_NAME,
        issued_at: new Date().toISOString(),
        handle: {
          ...payload,
          base_sha: boot.base_sha,
          model: runningModel,
          harness: bootedHarness(boot),
          process_id: running.id,
          launched: true,
          detail: "adopted: this container's work process was already running",
        },
      },
      adopted: true,
    };
  }

  const boot = await deps.boot(spec);
  // The boot is recorded BEFORE the container is addressed (deps.boot composes
  // inputs; it touches no container), so a live work process can never exist
  // without a durable record of the boot that started it — the record an
  // adoption reads above, and the only honest answer to which model the
  // running container is on (tick dyo).
  await deps.boots.record({
    run_id: spec.run_id,
    tick_id: spec.tick_id,
    attempt: spec.attempt,
    job: slot ?? "",
    model: bootedModel(boot),
    at: new Date().toISOString(),
  });
  const work = workerWorkSpec(boot);
  const task = { tick_id: spec.tick_id, branch: landing, base_sha: boot.base_sha };
  const spawned = await spawnWorker(deps.binding, name, task, work, deps.spawn);
  if (deps.jobLogs !== undefined && spawned.launched && spawned.process_id !== null) {
    // Where the confirm window's copy ended, so the state route continues the
    // stream rather than repeating it (tick 86y). Best effort: a cursor that
    // could not be written means the first drain copies from the start.
    try {
      await deps.jobLogs.started(spec, spawned.process_id, spawned.output_offset ?? 0);
    } catch (error) {
      console.error(
        `factory sandbox door: could not record ${name}'s log cursor: ${String(error)}`,
      );
    }
  }
  return {
    handle: {
      schema_version: JOB_HANDLE_SCHEMA_VERSION,
      job_id: jobID,
      attempt: spec.attempt,
      executor: SANDBOX_EXECUTOR_NAME,
      issued_at: new Date().toISOString(),
      handle: {
        ...payload,
        base_sha: boot.base_sha,
        model: bootedModel(boot),
        harness: bootedHarness(boot),
        process_id: spawned.process_id,
        launched: spawned.launched,
        detail: spawned.detail,
      },
    },
    adopted: false,
  };
}

/**
 * The process id a hosted attempt's log is drained under: the WorkerAgent's
 * log is one stream per attempt, whatever container processes it ran.
 */
export const WORKER_AGENT_LOG_ID = "worker-agent";

/**
 * Starts one attempt on its WorkerAgent (epic 43y, tick xd3): the same
 * identity, the same recorded boot, the same handle — but the attempt's
 * boot, conversation and finish are the agent's to drive, and the door
 * returns as soon as the agent has recorded it.
 *
 * An agent that already holds the attempt is an ADOPTION, for the same
 * reason a live work process is one: a door retry must never start a second
 * worker over a running one. Its handle names the model of the recorded boot
 * (tick dyo), and the agent's own record when the boot record is missing.
 */
async function startHostedAttempt(
  deps: SandboxExecutorDeps,
  spec: AttemptSpec,
  hosting: WorkerAgentHosting,
  ids: {
    jobID: string;
    slot: string | undefined;
    name: string;
    payload: Omit<SandboxHandlePayload, "process_id" | "launched" | "detail" | "model" | "harness">;
  },
): Promise<{ handle: SandboxJobHandle; adopted: boolean }> {
  const agent = hosting.agent(ids.name);
  const handleOf = (boot: WorkerBootInput, model: string, detail: string): SandboxJobHandle => ({
    schema_version: JOB_HANDLE_SCHEMA_VERSION,
    job_id: ids.jobID,
    attempt: spec.attempt,
    executor: SANDBOX_EXECUTOR_NAME,
    issued_at: new Date().toISOString(),
    handle: {
      ...ids.payload,
      base_sha: boot.base_sha,
      model,
      harness: bootedHarness(boot),
      // No one container process is the attempt: its boot, its tools and its
      // finish are each their own, and the agent drives them.
      process_id: null,
      launched: true,
      detail,
    },
  });

  const state = await agent.state();
  // An attempt the agent holds and has NOT settled is adopted; a settled one
  // is started afresh below, as a fresh boot under a settled container's name
  // is (its boot record replaces the old one and clears the settlement).
  if (state.phase !== "absent" && state.phase !== "settled") {
    const recorded = await deps.boots.modelOf(spec);
    const runningModel =
      recorded !== null && recorded.trim() !== "" ? recorded : (state.model ?? "");
    if (runningModel.trim() === "") throw new AdoptionModelUnknownError(spec);
    const boot = await deps.boot(spec);
    return {
      handle: handleOf(
        boot,
        runningModel,
        `adopted: this attempt's WorkerAgent already holds it (${state.phase})`,
      ),
      adopted: true,
    };
  }

  const boot = await deps.boot(spec);
  // Recorded BEFORE the agent is started, as a container boot is: an agent
  // can never hold an attempt with no durable record of what it was booted on.
  await deps.boots.record({
    run_id: spec.run_id,
    tick_id: spec.tick_id,
    attempt: spec.attempt,
    job: ids.slot ?? "",
    model: bootedModel(boot),
    at: new Date().toISOString(),
  });
  // The agent's log is one stream per attempt, from its start: the state
  // route continues it from 0 (a fresh start under a settled name included).
  if (deps.jobLogs !== undefined) {
    try {
      await deps.jobLogs.started(spec, WORKER_AGENT_LOG_ID, 0);
    } catch (error) {
      console.error(
        `factory sandbox door: could not reset ${ids.name}'s log cursor: ${String(error)}`,
      );
    }
  }
  await agent.start({
    name: ids.name,
    tick: spec.tick_id,
    role: spec.role,
    env: workerBootEnv(boot),
    model: bootedModel(boot),
    repoUrl: boot.repo_url,
    baseSha: boot.base_sha,
    ...(boot.harness_budget_ms === undefined ? {} : { wallMs: boot.harness_budget_ms }),
    boot: hosting.boot(ids.name),
  });
  return {
    handle: handleOf(
      boot,
      bootedModel(boot),
      "started: the attempt's WorkerAgent drives it on pi-durable — the container's boot phase, " +
        "the conversation, the finish phase",
    ),
    adopted: false,
  };
}

/**
 * The model a container booted from these inputs runs on: the `TICKS_MODEL`
 * its environment carries, derived by the same function that builds that
 * environment, so the handle cannot name one model while the container was
 * told another.
 */
function bootedModel(boot: WorkerBootInput): string {
  return workerBootEnv(boot).TICKS_MODEL ?? "";
}

/**
 * The harness a container booted from these inputs binds: the `TICKS_HARNESS`
 * its environment carries, derived by the same function that builds that
 * environment (tick 9iz), so the handle cannot name one harness while the
 * container was told another.
 */
function bootedHarness(boot: WorkerBootInput): string {
  return workerBootEnv(boot).TICKS_HARNESS ?? "";
}

// ---------------------------------------------------------------- inspect ---

/**
 * Maps the seam's process view onto the JobStatus the contract pins
 * (job-protocol $defs.job_status, tick us2): `running` while it works,
 * `succeeded`/`failed` with the exit code riding an `exited` observation once
 * terminal, and `lost` — deliberately NOT terminal, an statement about the
 * observer — when nobody can address the container at all.
 *
 * Pure, so the mapping is testable without a binding — the vocabulary is the
 * compatibility claim this executor makes ("the same four operations, the
 * same records"), and a mapping exercisable only against a live container is
 * a mapping nobody tests.
 */
export function statusFromProcess(
  view: { state: SandboxProcessState; exit_code: number | null } | null,
  jobID: string,
  now: () => string = () => new Date().toISOString(),
): AttemptStatus {
  const observedAt = now();
  if (view === null) {
    return {
      schema_version: 1,
      job_id: jobID,
      state: "lost",
      terminal: false,
      observed_at: observedAt,
      cursor: null,
    };
  }
  if (view.state === "running") {
    return {
      schema_version: 1,
      job_id: jobID,
      state: "running",
      terminal: false,
      observed_at: observedAt,
      cursor: null,
    };
  }
  return {
    schema_version: 1,
    job_id: jobID,
    state: view.exit_code === 0 ? "succeeded" : "failed",
    terminal: true,
    observed_at: observedAt,
    cursor: null,
    observations: [
      {
        at: observedAt,
        kind: "exited",
        detail:
          view.exit_code === null
            ? "the container's work process ended with no exit status — killed, or its container " +
              "stopped under it (idled out, evicted or reclaimed): it is not running, and whatever " +
              "it pushed is on its landing branch"
            : `the container's work process exited ${view.exit_code}`,
      },
    ],
  };
}

/**
 * Reports what can be SEEN of one attempt, re-addressing the container by
 * name: the work process by id, and — when the id answers nothing — the
 * live process list, because "no id" is an evidence gap and not an absence.
 */
async function inspectAttempt(
  deps: SandboxExecutorDeps,
  handle: SandboxJobHandle,
): Promise<AttemptStatus> {
  const payload = handle.handle;
  const sandbox = await namedSandbox(deps.binding, payload.sandbox);
  if (payload.process_id !== null) {
    const view = await sandbox.getProcess(payload.process_id);
    if (view !== null) return statusFromProcess(view, handle.job_id);
    // The id answered nothing. The list is the second evidence source, for
    // exactly the id-less case: a container that died and came back, or a
    // supervisor that never recorded the id. Only a list with no work
    // process in it is "lost".
    const running = await findWorkProcess(sandbox);
    if (running !== null)
      return statusFromProcess({ state: "running", exit_code: null }, handle.job_id);
  } else {
    const running = await findWorkProcess(sandbox);
    if (running !== null)
      return statusFromProcess({ state: "running", exit_code: null }, handle.job_id);
  }
  return statusFromProcess(null, handle.job_id);
}

// ----------------------------------------------------------------- read ---

/**
 * What one attempt's named container looks like from IDENTITY alone — no
 * handle, nothing persisted by a caller that may since have died (tick 8ty).
 *
 * This is the operation the HTTP door's state route is: a restarted
 * orchestrator that re-derived its state from git has an identity (run, tick,
 * attempt) and no handle, and the question it needs answered is the one the
 * tick spells: running, finished with a result, or absent. The container is
 * re-addressed BY NAME — the same no-live-object-across-a-boundary rule the
 * whole module is built around, restated for a caller that is on the far
 * side of HTTP from the binding.
 *
 * The mapping, in the job_status contract's own vocabulary so the caller
 * reads one vocabulary across executors:
 *
 *  - `running`    the named container holds a live work process.
 *  - `succeeded`/`failed` — FINISHED, terminal, the exit code riding an
 *                  `exited` observation: the result that exists at this
 *                  layer. The tick's report lives in git, not here.
 *  - `lost`       ABSENT — no container under this identity holds a work
 *                  process: never started, or its record is `gone`. The
 *                  contract's own words for `lost` are the point: it is a
 *                  statement about the observer, not a verdict on the job,
 *                  and recovery may re-adopt. A caller must never read it as
 *                  "finished" and write the attempt off.
 */
export async function namedAttemptStatus(
  binding: SandboxBinding,
  identity: { run_id: string; tick_id: string; attempt: number; job_id?: string },
  jobID: string,
  now: () => string = () => new Date().toISOString(),
  records?: SandboxJobRecords,
  logs?: SandboxJobLogs,
  agents?: WorkerAgentResolver,
): Promise<AttemptStatus> {
  // The records first, and the container only when they cannot answer
  // (epic hn6's second cloud run). Through the SDK, ANY call on a container
  // that is not running STARTS it, so a status read that addressed the
  // container of a job nobody booted cold-booted one — and waited for a free
  // instance while the orchestrator and settled workers held them all, until
  // the caller's client gave up. A job with no recorded boot has no work
  // process (the boot is recorded before its container is addressed), and a
  // settled job answers from its settlement: neither needs a container.
  if (records !== undefined) {
    const settled = await records.settled(identity);
    if (settled !== null) return statusFromProcess(settled, jobID, now);
    if (!(await records.booted(identity))) return statusFromProcess(null, jobID, now);
  }
  // Re-addressed by the FULL job id, never (run, tick, attempt) alone: a
  // settled attempt's container must not answer for the repair of it.
  const name = attemptSandboxName(
    identity.run_id,
    identity.tick_id,
    identity.attempt,
    identity.job_id,
  );
  // A hosted attempt (epic 43y, tick xd3) answers from its WorkerAgent, never
  // from its container: the container is only where its tools run, and is
  // stopped and restored under a live attempt by design.
  const hosting = agents === undefined ? null : await agents(identity.run_id);
  if (hosting !== null) {
    return hostedAttemptStatus(hosting.agent(name), identity, jobID, now, records, logs);
  }
  const sandbox = await namedSandbox(binding, name);
  // A booted, unsettled job whose container is NOT RUNNING has no work
  // process anywhere: a process lives only inside its container, and the
  // container stopped under it — idled out, evicted, or reclaimed. That is
  // the factory's own knowledge, and it is a verdict, not an evidence gap
  // (epic hn6, run_6d88e3de: 7uv's container idled out while the
  // orchestrator sat in another job's wait, the next read cold-booted it
  // empty, and `lost` — "nobody can say whether it is running" — halted the
  // run for a person over a worker the factory could have said was gone).
  // Asked BEFORE the list, because the list would boot the container just to
  // find it empty. Settled `failed` in the records, so every later read
  // answers the same and the slot is no longer counted held; whatever the
  // worker pushed is on its landing branch, where collect reads it.
  if (records !== undefined && sandbox.isRunning !== undefined && !(await sandbox.isRunning())) {
    const stopped = { state: "failed" as const, exit_code: null };
    await records.settle(identity, stopped);
    return statusFromProcess(stopped, jobID, now);
  }
  // The list, never a remembered id — the evidence-gap rule the seam documents
  // and `inspectAttempt` already leans on: a caller with no handle has no id
  // to ask about, and "I have no id for it" must never read as "nothing is
  // running here". The LAST work process in the list is the live one when
  // there is one: adoption means a container holds at most one.
  const listed = await sandbox.listProcesses();
  let work: { id: string; state: SandboxProcessState; exit_code: number | null } | null = null;
  for (const view of listed ?? []) {
    if (isWorkProcess(view)) work = view;
  }
  if (work === null || work.state === "gone") {
    // Absent — and `lost`, never `failed`: a `gone` record is a container that
    // lost its own knowledge of the process, which is an evidence gap about
    // the OBSERVER, and the job's fate is exactly as unknown as when nothing
    // was ever started.
    return statusFromProcess(null, jobID, now);
  }
  // What the worker printed since the last look goes to the run's log on
  // every look (tick 86y) — and for a settled worker, to its END, before the
  // reclaim below destroys the container and its output with it.
  await drainJobLog(logs, identity, sandbox, work.id);
  if (work.state === "running") {
    return statusFromProcess({ state: "running", exit_code: null }, jobID, now);
  }
  const settled = { state: work.state, exit_code: work.exit_code };
  if (records !== undefined) {
    // Settled: the verdict is recorded FIRST, then the container reclaimed.
    // Its work is on the landing branch (the entrypoint pushes before it
    // exits) and collect reads git, never the container — so a settled
    // worker's container is only capacity held, and left up it idles out
    // after SANDBOX_SLEEP_AFTER at the earliest, every status poll renewing
    // it. A reclaim that fails is not the verdict's to inherit: the record
    // stands, and the container still idles out on its own.
    await records.settle(identity, settled);
    try {
      await sandbox.destroy();
    } catch (error) {
      console.error(
        `factory sandbox door: could not reclaim settled container ${name}: ${String(error)}`,
      );
    }
  }
  return statusFromProcess(settled, jobID, now);
}

/**
 * A hosted attempt's state, from its WorkerAgent (epic 43y, tick xd3) — the
 * same job_status vocabulary a container's work process answers in:
 *
 *  - an agent holding no attempt is `lost` (booted, never started: the start
 *    died between the boot record and the agent) — not terminal, for the
 *    contract's reason: a start under the same identity starts it;
 *  - an unsettled attempt is `running`, its log copied on every look;
 *  - a settled one is `succeeded`/`failed` with the attempt's exit code —
 *    the finish phase's, which is what the all-in-one's exit code was —
 *    recorded first, then its container destroyed: its work is on its
 *    branch, and collect reads git.
 */
async function hostedAttemptStatus(
  agent: WorkerAgentStub,
  identity: SandboxJobIdentity,
  jobID: string,
  now: () => string,
  records: SandboxJobRecords | undefined,
  logs: SandboxJobLogs | undefined,
): Promise<AttemptStatus> {
  const state = await agent.state();
  if (state.phase === "absent") return statusFromProcess(null, jobID, now);
  await drainJobLog(
    logs,
    identity,
    { readOutput: (_id, offset) => agent.readLog(offset) },
    WORKER_AGENT_LOG_ID,
  );
  if (state.phase !== "settled") {
    return statusFromProcess({ state: "running", exit_code: null }, jobID, now);
  }
  const settled = {
    state: state.exit_code === 0 ? ("completed" as const) : ("failed" as const),
    exit_code: state.exit_code,
  };
  if (records !== undefined) {
    await records.settle(identity, settled);
    try {
      await agent.release();
    } catch (error) {
      console.error(
        `factory sandbox door: could not release ${identity.tick_id} attempt ${identity.attempt}'s ` +
          `container: ${String(error)}`,
      );
    }
  }
  return statusFromProcess(settled, jobID, now);
}

/**
 * The durable records the door's state route answers from before it addresses
 * a container (migrations 0018 and 0019): whether a job's container was ever
 * booted, and the terminal state it settled in. Behind a seam so the route's
 * "never boot a container to answer a question" rule is testable without D1.
 */
export type SandboxJobRecords = {
  booted(identity: SandboxJobIdentity): Promise<boolean>;
  settled(
    identity: SandboxJobIdentity,
  ): Promise<{ state: "completed" | "failed"; exit_code: number | null } | null>;
  settle(
    identity: SandboxJobIdentity,
    settled: { state: SandboxProcessState; exit_code: number | null },
  ): Promise<void>;
};

type SandboxJobIdentity = { run_id: string; tick_id: string; attempt: number; job_id?: string };

/** The job records over the factory's own D1. */
export function d1JobRecords(db: D1Database): SandboxJobRecords {
  const key = (identity: SandboxJobIdentity) => ({
    run_id: identity.run_id,
    tick_id: identity.tick_id,
    attempt: identity.attempt,
    job: attemptJobSlot(identity.run_id, identity.tick_id, identity.attempt, identity.job_id) ?? "",
  });
  return {
    async booted(identity) {
      return (await sandboxAttemptBootModel(db, key(identity))) !== null;
    },
    async settled(identity) {
      return sandboxJobSettled(db, key(identity));
    },
    async settle(identity, settled) {
      if (settled.state !== "completed" && settled.state !== "failed") return;
      await recordSandboxJobSettled(db, {
        ...key(identity),
        state: settled.state,
        exit_code: settled.exit_code,
        at: new Date().toISOString(),
      });
    },
  };
}

// ---------------------------------------------------------------- collect ---

/**
 * Maps a `WorkerReport` — collect's durable-layer verdict — onto the
 * `AttemptReport` the reconciler settles a tick from.
 *
 * Pure, for `statusFromProcess`'s reason. The vocabulary correspondence:
 *
 *  - `done`    ready-to-merge with a report whose status line is DONE or
 *              DONE_WITH_CONCERNS — the tick's work survived in git and said
 *              so. The concerns ride the detail, where the review reads
 *              them; they do not change the outcome, for the same reason
 *              they do not locally.
 *  - `blocked` a status a human must read (BLOCKED, NEEDS_CONTEXT). The
 *              reconciler refuses the run on one of these rather than
 *              draining the window, which is what `blocked` exists to say.
 *  - `failed`  everything else — no commits, no report, a boundary
 *              violation, or an unreadable remote (`unknown`): an attempt
 *              that left nothing is redispatched, and an unreadable one is
 *              never reported as a clean failure of the work.
 */
export function reportFromWorker(report: WorkerReport): AttemptReport {
  if (report.verdict === "ready-to-merge" && !needsHuman(report)) {
    const concerns = report.status === "DONE_WITH_CONCERNS" ? " (with concerns)" : "";
    return {
      outcome: "done",
      commits: report.commits,
      detail: `${report.verdict}${concerns}: ${report.status_line || report.detail}`,
    };
  }
  if (needsHuman(report)) {
    return {
      outcome: "blocked",
      commits: report.commits,
      detail: `${report.status} on ${report.branch}: ${report.status_detail || report.detail}`,
    };
  }
  if (report.verdict === "unknown") {
    return {
      outcome: "failed",
      commits: 0,
      detail: `the durable layer could not be read (${report.detail}); the evidence is not a verdict`,
    };
  }
  // A report-only refusal carries the collect's own sentence, never the
  // worker's status line (tick 94u): the line would read "no-commits:
  // STATUS: DONE" — the collapsed message A9 refuses, the worker's claim
  // where the operator needs the diagnosis. The sentence names the one
  // commit there was.
  if (report.report_only) {
    return {
      outcome: "failed",
      commits: report.commits,
      detail: `${report.verdict}: ${report.detail}`,
    };
  }
  return {
    outcome: "failed",
    commits: report.commits,
    detail: `${report.verdict}: ${report.status_line || report.detail}`,
  };
}

/**
 * Collects one attempt from the durable layer — the attempt's own write_ref,
 * the report it carries, the boundary it kept — and never from the container
 * itself, which by now may be gone.
 *
 * The push comes first (tick us2): the container landed its work on the
 * per-attempt landing branch (the image derives the branch, and the attempt
 * rides the epic slot of the boot), and the attempt's identity is its
 * write_ref — the ref the marker names, the settle's integrate call names,
 * and a person reading the run names. The executor puts the landing branch's
 * head there, the same push the local executor makes itself, and then reads
 * the collect from the write_ref: one ref, named by everything. A put that
 * was REFUSED is never reported as a clean verdict on the work — the same
 * rule `unknown` keeps below.
 */
async function collectAttempt(
  deps: SandboxExecutorDeps,
  handle: SandboxJobHandle,
): Promise<AttemptReport> {
  const payload = handle.handle;
  // The base and run ride the put so it reads the branch the container
  // ACTUALLY pushed — never another run's branch that holds the landing name
  // (epic hn6's second cloud run; git-refs.ts GitRefWriter).
  const put = await deps.refs.put({
    branch: payload.branch,
    ref: payload.write_ref,
    base_sha: payload.base_sha,
    run_id: payload.run_id,
  });
  if (put.state === "refused") {
    return {
      outcome: "failed",
      commits: 0,
      detail:
        `the attempt's write ref ${payload.write_ref} could not be advanced from ` +
        `${payload.branch}: ${put.detail} — the evidence is not a verdict`,
    };
  }
  const report = await deps.collector.collect({
    tick_id: payload.tick_id,
    branch: writeRefBranch(payload.write_ref),
    base_sha: payload.base_sha,
    // Where the container itself pushed: a boot that stopped before its
    // harness leaves its reason beside this branch, not the write_ref (#176).
    landing_branch: payload.branch,
    run_id: payload.run_id,
  });
  return reportFromWorker(report);
}

// ----------------------------------------------------------------- cancel ---

/**
 * Cancels one attempt by asking first and destroying second.
 *
 * The ask is the container's own stop-and-push door (tick 7zk): a second
 * process which lodges the request and stops the harness, so the
 * entrypoint's existing salvage path — commit, report, push — runs as it
 * does at its own bound. `teardownWorker` then re-checks liveness itself
 * (expiring, never stale) and reclaims the container either way. A cancel
 * that only killed would be the $8.00-for-nothing failure the door exists
 * to end; one that only asked would leave the container on the clock.
 */
async function cancelAttempt(deps: SandboxExecutorDeps, handle: SandboxJobHandle) {
  const payload = handle.handle;
  const boot = await deps.boot({
    run_id: payload.run_id,
    epic_id: payload.epic_id,
    tick_id: payload.tick_id,
    attempt: handle.attempt,
    job_id: handle.job_id,
    role: payload.role,
    project: payload.project,
    write_ref: payload.write_ref,
    base_ref: payload.base_ref,
    title: payload.title,
    // The harness the container was booted on (tick 9iz): the salvage door
    // runs in the same container's environment, and a re-derived boot that
    // disagreed with the recorded one would knock on a door built for a
    // worker that is not the one running.
    harness: payload.harness,
  });
  const work = workerWorkSpec(boot);
  const salvage: SalvageSpec | undefined = work.salvage;
  await salvageWorker(deps.binding, payload.sandbox, payload.process_id, salvage, {
    reason: "stopped:run",
    ...(deps.spawn?.sleep === undefined ? {} : { sleep: deps.spawn.sleep }),
  });
  await teardownWorker(deps.binding, payload.sandbox, payload.process_id);
}

// --------------------------------------------------------------- the whole ---

/**
 * Builds the compatibility executor over the seams: the same four operations
 * the local executors implement, with the SandboxBinding's own behaviour —
 * green-start traps, id-less adoption, git-only collects, salvage doors —
 * behind each one.
 */
export function sandboxExecutor(deps: SandboxExecutorDeps): AttemptExecutor {
  return {
    async start(spec: AttemptSpec): Promise<AttemptHandle> {
      return (await startNamedAttempt(deps, spec)).handle as unknown as AttemptHandle;
    },
    async inspect(record: AttemptHandle): Promise<AttemptStatus> {
      return inspectAttempt(deps, jobHandleOf(record));
    },
    async collect(record: AttemptHandle): Promise<AttemptReport> {
      return collectAttempt(deps, jobHandleOf(record));
    },
    async cancel(record: AttemptHandle): Promise<void> {
      await cancelAttempt(deps, jobHandleOf(record));
    },
  };
}

// ------------------------------------------------- the deployment wiring ---

/**
 * What the executor needs from the run it serves, beyond what `Env` carries.
 *
 * `base_sha` is the epic base: the commit every worker clones at and every
 * collect compares its branch against. The Workflow host is the one party
 * that knows it — it is what the run was submitted against — and collect
 * without it would compare a branch against nothing.
 */
export type SandboxExecutorEnvInput = {
  project: string;
  base_sha?: string;
  /**
   * The run whose containers this executor boots, when the caller knows it
   * (tick 9iz): the door names it, because a dispatch's worker log stream is
   * the RUN's — one stream per tick under the run's own artifacts, exactly
   * where the wave path put them and the log read routes read them. Absent
   * means no streaming, which is the Workflow's own dispatch (no run id at
   * the seam that builds an executor for it).
   */
  run_id?: string;
};

/**
 * The executor's wiring, stated as an answer either way (tick 8ty).
 *
 * `sandboxExecutorFromEnv` could not serve the HTTP dispatch door
 * (`sandbox-dispatch.ts`): the door needs the SEAMS — to call
 * `startNamedAttempt` directly and learn whether a dispatch adopted — not
 * the closed four-operation interface, and a door that cannot boot a
 * container must refuse with the missing pieces in its response body rather
 * than only on the Worker's log. So the wiring is factored into a function
 * that names its own refusal, and both callers — the Workflow's executor and
 * the door — share one diagnosis of a deployment that cannot dispatch.
 */
export type SandboxExecutorWiring = { deps: SandboxExecutorDeps } | { refusal: string };

/**
 * The seams a dispatch needs, built from the deployment's own bindings — or
 * the sentence that says which pieces are missing.
 *
 * The boot inputs are composed the way the orchestrator's boots are
 * (`run-workflow.ts`): a run-scoped gateway token minted per dispatch, and
 * git access through `planSandboxGit`'s write grade — the repository itself
 * on github.com with the operator's credential, exactly what a write run
 * has always been handed. A deployment missing any piece (the container
 * binding, the factory's own base URL, the base the containers clone at)
 * gets the refusal naming every gap.
 *
 * The token is minted per boot WITHOUT revocation (tick 53s):
 * `issueWorkerRunToken`, not the orchestrator's rotating `issueRunToken`.
 * A run holds several of this executor's workers at once
 * (`max_parallel > 1`), and the rotating issue revoked every live token the
 * run held — so the second worker's boot cut the first off mid-tick with
 * 403 run_token_revoked, and even this executor's own ADOPTION and CANCEL
 * boots (which re-derive their inputs the same way) killed the very worker
 * they were adopting or leaving running. Rotation is the orchestrator's
 * rule because a run holds ONE orchestrator; a worker's lifetime ends at
 * the run's kill switch (revokeRunTokens), never at a sibling's boot.
 *
 * The boot carries the ATTEMPT (tick us2): the container's branch is
 * derived inside the image from the epic and tick slots, and the attempt
 * rides the epic slot so every attempt lands its work on a branch of its
 * own — the cloud's half of the one-ref-per-attempt rule.
 */
export function sandboxExecutorDepsFromEnv(
  env: Env,
  input: SandboxExecutorEnvInput,
): SandboxExecutorWiring {
  const binding = sandboxBinding(env);
  const missing: string[] = [];
  if (binding === null) {
    missing.push(
      "the SANDBOXES container binding (a deployment that cannot boot a container cannot run anything)",
    );
  }
  const base = (input.base_sha ?? "").trim();
  if (base === "") {
    missing.push(
      "the base SHA the attempt's worker clones at (the commit this dispatch was derived from)",
    );
  }
  const factory = factoryBaseURL(env);
  if (factory === null) {
    missing.push("FACTORY_BASE_URL (the run gateway and the git door are addressed through it)");
  }
  if (env.DB === undefined || env.DB === null) {
    missing.push("the D1 binding (run gateway credentials are minted and revoked through it)");
  }
  if (missing.length > 0) {
    return { refusal: missing.join("; ") };
  }

  const agents = workerAgentsFromEnv(env);
  const git = planSandboxGit({
    grade: "write",
    project: input.project,
    operator_token: env.GITHUB_TOKEN,
    factory_url: factory,
    direct_repo_url: repoURL(input.project),
  });
  if (!git.ok) {
    return { refusal: git.detail };
  }

  return {
    deps: {
      binding: binding as SandboxBinding,
      collector: workerCollector(env, input.project),
      // The attempt's own push goes through the deployment's git-data writer,
      // or the seam a test injects (TICFAC_REF_WRITER) — same pattern as
      // WORKER_COLLECTOR: the ordering is what needs testing.
      refs: env.TICFAC_REF_WRITER ?? gitRefWriter(env, input.project),
      // Each container's own stdout/stderr, streamed to the run's R2 stream
      // as it appears (tick 9iz) — the same sink the wave path wired into
      // every spawn, so a worker dispatched through the door leaves the log
      // the run's read routes serve instead of taking its diagnosis with it
      // when the container goes. The wave's wait loop streamed the tail too;
      // the door's answer cannot hold a stream open, so what its spawn window
      // (the probe and the confirmed dispatch) observes is what streams.
      ...(env.ARTIFACTS === undefined || input.run_id === undefined
        ? {}
        : {
            spawn: { logs: workerLogSink(env.ARTIFACTS, input.project, input.run_id) },
            // The rest of the stream, past the confirm window: the state route
            // continues it from where the spawn's copy ended (tick 86y).
            jobLogs: d1JobLogs(env.DB as D1Database, env.ARTIFACTS, input.project),
          }),
      boots: d1BootRecord(env.DB),
      // Every worker of a do_v1 run is a WorkerAgent (epic 43y, tick xd3).
      ...(agents === undefined ? {} : { agents }),
      boot: async (spec) => {
        const slot = attemptJobSlot(spec.run_id, spec.tick_id, spec.attempt, spec.job_id);
        // Minted per dispatch, revoking NOTHING (tick 53s): the run's workers
        // are parallel spenders, so a boot that rotated would cut every live
        // sibling off at the next worker's start — including this executor's
        // own adoption and cancel boots, which arrive while other workers are
        // mid-tick. The credential is still per worker, never shared across
        // attempts: one revocation cannot take it back from just this attempt.
        // The worker's GitHub credential, per boot (epic dm6): the rung's
        // token for this run's repository and, on the App rung, where the
        // worker refreshes it. Asked before the run token is minted, so a
        // refusal spends nothing.
        const github = await containerGitHub(env, input.project, git.plan, factory);
        if (!github.ok) throw new Error(github.denial.detail);
        const credential = await issueWorkerRunToken(env, {
          run_id: spec.run_id,
          tick_id: spec.tick_id,
          attempt: spec.attempt,
        });
        return {
          repo_url: git.plan.repo_url,
          base_sha: base,
          epic: spec.epic_id,
          tick: spec.tick_id,
          attempt: spec.attempt,
          ...(slot === undefined ? {} : { job: slot }),
          run_id: spec.run_id,
          gateway_base_url: runGatewayEndpoint(factory as string),
          gateway_token: credential.token,
          // The dispatch's own harness and prompt (tick 9iz): the door carries
          // the harness the caller's profile resolved and the rendered role
          // prompt beside it, and a choice about this attempt outranks the
          // deployment's standing one — the same ladder the model rides.
          harness: workerHarness(spec.harness ?? null, textVar(env, "RUN_WORKER_HARNESS")),
          // The dispatch's own model first (tick a08): the door carries the
          // model the caller's profile resolved, and a choice about this
          // attempt outranks the deployment's standing one.
          model: workerModel(spec.model ?? null, textVar(env, "RUN_WORKER_MODEL")),
          prompt: spec.prompt,
          // A carried attempt's work base (epic hn6, run_3f034e68): the
          // container measures the carried work from it, so a worker that
          // found it complete and added nothing settles succeeded.
          ...(spec.work_base_sha === undefined ? {} : { work_base_sha: spec.work_base_sha }),
          // The dispatch's wall, less the push margin (tick 86y): the door
          // bounded no worker's harness where the wave path bounded every one.
          ...(workerHarnessBudgetMs(spec.wall_seconds) === undefined
            ? {}
            : { harness_budget_ms: workerHarnessBudgetMs(spec.wall_seconds) }),
          github_token: containerGitToken(git.plan, github.token, credential.token),
          ...(github.token_url === undefined ? {} : { github_token_url: github.token_url }),
          sandbox_image: deploymentImage(env),
          factory_url: factory as string,
          factory_token: credential.token,
          factory_project: input.project,
        };
      },
    },
  };
}

/**
 * The boot record over the factory's own D1 (tick dyo): the deployed wiring
 * both the door and a Workflow-side executor share, behind the seam so the
 * adoption's read is testable without a database.
 */
export function d1BootRecord(db: D1Database): SandboxBootRecord {
  return {
    async record(boot: SandboxAttemptBoot) {
      await recordSandboxAttemptBoot(db, boot);
    },
    async modelOf(identity: { run_id: string; tick_id: string; attempt: number; job_id?: string }) {
      return sandboxAttemptBootModel(db, {
        run_id: identity.run_id,
        tick_id: identity.tick_id,
        attempt: identity.attempt,
        job:
          attemptJobSlot(identity.run_id, identity.tick_id, identity.attempt, identity.job_id) ??
          "",
      });
    },
  };
}

/**
 * The executor a deployment dispatches through, built from the deployment's
 * own bindings — or undefined, naming what is missing, so the Workflow
 * refuses a dispatch on a stated gap rather than on an executor nobody
 * configured. See {@link sandboxExecutorDepsFromEnv} for how the boots this
 * executor mints are composed.
 */
export function sandboxExecutorFromEnv(
  env: Env,
  input: SandboxExecutorEnvInput,
): AttemptExecutor | undefined {
  const wired = sandboxExecutorDepsFromEnv(env, input);
  if ("refusal" in wired) {
    console.error(`factory sandbox-executor: no attempt executor is wired — ${wired.refusal}`);
    return undefined;
  }
  return sandboxExecutor(wired.deps);
}
