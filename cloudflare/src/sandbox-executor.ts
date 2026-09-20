/**
 * The sandbox compatibility executor (SPEC §12 Phase 4 item 4, tick k4s):
 * the cloud host's concrete `AttemptExecutor`, built over the
 * `SandboxBinding` seam `sandbox.ts` already declares.
 *
 * The reconciler that dispatches through it is the Workflow-hosted
 * `EpicReconciler` (`epic-reconciler.ts`, item 2), whose `AttemptExecutor`
 * interface is contracts/job-protocol.json's four operations — start,
 * inspect, collect, cancel — the same seam every local executor implements.
 * Until this module, a deployment had no executor at all and the Workflow
 * refused every dispatch, exactly the way the local reconciler refuses a
 * profile naming an executor its build cannot honour: this module is what
 * turns `cloudflare-sandbox` from a name the tracker's enum merely admits
 * into an executor a run actually dispatches through.
 *
 * What each operation is built from — every piece an existing, live-proven
 * module, none of it invented here:
 *
 *  - `start`    `worker-boot.ts` composes the boot (command, env, probe,
 *               salvage door) and `worker-dispatch.ts`'s `spawnWorker` runs
 *               the green-start trap and the confirmed-dispatch wait against
 *               a fresh container named per ATTEMPT.
 *  - `inspect`  the SandboxBinding's process view, with the rule
 *               `src/reconcile.ts` and `OrchestratorSandbox.listProcesses`
 *               exist for: "I have no id for it" must never be allowed to
 *               read as "nothing is running here".
 *  - `collect`  `worker-collect.ts`, which reads ONLY what survived in git —
 *               never a sandbox's terminal output — the collect rule all
 *               three substrates share.
 *  - `cancel`   the container's own stop-and-push door (`worker-boot.ts`'s
 *               salvage spec, tick 7zk) and then `teardownWorker`: the work
 *               is asked to rescue itself before the container is destroyed.
 *
 * THE HANDLE IS THE CONTRACT'S (tick us2). What `start` returns is a
 * job-protocol `JobHandle` — the closed top level (schema_version, job_id,
 * attempt, executor, issued_at) and the one open `handle` object the contract
 * reserves for executor-private addressing — validated against the pinned
 * `$defs.job_handle` by this module's tests, so a cloud-produced handle is
 * indistinguishable from a local one to everything that reads records. The
 * reconciler nests it under the dispatch marker's `handle` slot, the same
 * slot the run-state contract's own golden attempt record uses for herdr's
 * addressing. It deliberately carries NO credential: the handle is what the
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

import { containerGitToken, planSandboxGit } from "./credentials";
import type {
  AttemptExecutor,
  AttemptHandle,
  AttemptReport,
  AttemptSpec,
  AttemptStatus,
} from "./epic-reconciler";
import { factoryBaseURL, issueWorkerRunToken, runGatewayEndpoint } from "./gateway";
import { type GitRefWriter, gitRefWriter, writeRefBranch } from "./git-refs";
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
  attemptLandingBranch,
  WORKER_COMMAND,
  type WorkerBootInput,
  workerHarness,
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
export function attemptSandboxName(runID: string, tickID: string, attempt: number): string {
  return `${runID}-${tickID}-${attempt}`;
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
  /** Spawn knobs (sleep, log sinks, budgets) — the wave machinery's own. */
  spawn?: SpawnOptions;
};

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
 */
async function startAttempt(
  deps: SandboxExecutorDeps,
  spec: AttemptSpec,
): Promise<SandboxJobHandle> {
  const name = attemptSandboxName(spec.run_id, spec.tick_id, spec.attempt);
  // The per-attempt landing branch the container derives from the boot
  // (worker-boot.ts puts the attempt in the epic slot): the work the
  // container pushes lands on no other attempt's branch, so a redispatch
  // starts from the base it was given, not from the previous attempt's work.
  const landing = attemptLandingBranch(spec.epic_id, spec.attempt, spec.tick_id);
  const payload: Omit<SandboxHandlePayload, "process_id" | "launched" | "detail"> = {
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

  // Adoption first: a container already holding a live work process is this
  // attempt's, by the name nobody else would boot under, and starting a
  // second one beside it is how a run pays twice for one tick.
  const sandbox = await namedSandbox(deps.binding, name);
  const running = await findWorkProcess(sandbox);
  if (running !== null) {
    const boot = await deps.boot(spec);
    return {
      schema_version: JOB_HANDLE_SCHEMA_VERSION,
      job_id: `run-${spec.run_id}/tick-${spec.tick_id}/attempt-${spec.attempt}`,
      attempt: spec.attempt,
      executor: SANDBOX_EXECUTOR_NAME,
      issued_at: new Date().toISOString(),
      handle: {
        ...payload,
        base_sha: boot.base_sha,
        process_id: running.id,
        launched: true,
        detail: "adopted: this container's work process was already running",
      },
    };
  }

  const boot = await deps.boot(spec);
  const work = workerWorkSpec(boot);
  const task = { tick_id: spec.tick_id, branch: landing, base_sha: boot.base_sha };
  const spawned = await spawnWorker(deps.binding, name, task, work, deps.spawn);
  return {
    schema_version: JOB_HANDLE_SCHEMA_VERSION,
    job_id: `run-${spec.run_id}/tick-${spec.tick_id}/attempt-${spec.attempt}`,
    attempt: spec.attempt,
    executor: SANDBOX_EXECUTOR_NAME,
    issued_at: new Date().toISOString(),
    handle: {
      ...payload,
      base_sha: boot.base_sha,
      process_id: spawned.process_id,
      launched: spawned.launched,
      detail: spawned.detail,
    },
  };
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
        detail: `the container's work process exited ${view.exit_code ?? "unknown"}`,
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
  const put = await deps.refs.put({ branch: payload.branch, ref: payload.write_ref });
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
    role: payload.role,
    project: payload.project,
    write_ref: payload.write_ref,
    base_ref: payload.base_ref,
    title: payload.title,
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
      return (await startAttempt(deps, spec)) as unknown as AttemptHandle;
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
};

/**
 * The executor a deployment dispatches through, built from the deployment's
 * own bindings — or null, naming what is missing, so the Workflow refuses a
 * dispatch on a stated gap rather than on an executor nobody configured.
 *
 * The boot inputs are composed the way the orchestrator's boots are
 * (`run-workflow.ts`): a run-scoped gateway token minted per dispatch, and
 * git access through `planSandboxGit`'s write grade — the repository itself
 * on github.com with the operator's credential, exactly what a write run
 * has always been handed. A deployment missing any piece (the container
 * binding, the factory's own base URL, the epic base) gets no executor and
 * the reconciler's own refusal names it.
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
export function sandboxExecutorFromEnv(
  env: Env,
  input: SandboxExecutorEnvInput,
): AttemptExecutor | undefined {
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
      "the epic base SHA (EpicReconcilerParams.base_sha — the commit every worker clones at)",
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
    console.error(`factory sandbox-executor: no attempt executor is wired — ${missing.join("; ")}`);
    return undefined;
  }

  const git = planSandboxGit({
    grade: "write",
    project: input.project,
    operator_token: env.GITHUB_TOKEN,
    factory_url: factory,
    direct_repo_url: repoURL(input.project),
  });
  if (!git.ok) {
    console.error(`factory sandbox-executor: no attempt executor is wired — ${git.detail}`);
    return undefined;
  }

  return sandboxExecutor({
    binding: binding as SandboxBinding,
    collector: workerCollector(env, input.project),
    // The attempt's own push goes through the deployment's git-data writer,
    // or the seam a test injects (TICFAC_REF_WRITER) — same pattern as
    // WORKER_COLLECTOR: the ordering is what needs testing.
    refs: env.TICFAC_REF_WRITER ?? gitRefWriter(env, input.project),
    boot: async (spec) => {
      // Minted per dispatch, revoking NOTHING (tick 53s): the run's workers
      // are parallel spenders, so a boot that rotated would cut every live
      // sibling off at the next worker's start — including this executor's
      // own adoption and cancel boots, which arrive while other workers are
      // mid-tick. The credential is still per worker, never shared across
      // attempts: one revocation cannot take it back from just this attempt.
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
        run_id: spec.run_id,
        gateway_base_url: runGatewayEndpoint(factory as string),
        gateway_token: credential.token,
        harness: workerHarness(null, textVar(env, "RUN_WORKER_HARNESS")),
        model: workerModel(null, textVar(env, "RUN_WORKER_MODEL")),
        github_token: containerGitToken(git.plan, env.GITHUB_TOKEN, credential.token),
        sandbox_image: deploymentImage(env),
        factory_url: factory as string,
        factory_token: credential.token,
        factory_project: input.project,
      };
    },
  });
}
