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
 * The handle this executor returns is a job-protocol `JobHandle` in the same
 * shape a local attempt's carries — executor name, job id, attempt, write
 * ref — plus the cloud's own addressing (the container's name, the work
 * process's id, the epic base the branch is compared against), so a run's
 * attempt records are indistinguishable in shape from a local attempt's and
 * a resumed pass adopts one by identity either way. It deliberately carries
 * NO credential: the handle is what the run's marker stores on the run
 * branch, and a secret in a committed record is a leak by construction.
 * The boot inputs are re-derived from the seam at every use, the same way
 * every orchestrator boot rotates its credential.
 */

import { containerGitToken, planSandboxGit } from "./credentials";
import type {
  AttemptExecutor,
  AttemptHandle,
  AttemptReport,
  AttemptSpec,
  AttemptStatus,
} from "./epic-reconciler";
import { factoryBaseURL, issueRunToken, runGatewayEndpoint } from "./gateway";
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
  WORKER_COMMAND,
  type WorkerBootInput,
  workerBranch,
  workerHarness,
  workerModel,
  workerTask,
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
 * What this executor's four operations re-address an attempt by.
 *
 * Job-protocol's `handle` is the one open object in the contract ("a
 * Cloudflare workspace id and a local pid have nothing in common"), so the
 * fields a local attempt's handle carries keep their names and the cloud's
 * own addressing rides beside them. No credential, ever — see the header.
 */
export type SandboxAttemptHandle = {
  executor: typeof SANDBOX_EXECUTOR_NAME;
  job_id: string;
  attempt: number;
  try: number;
  tick_id: string;
  role: string;
  remote: string;
  resumed_from: null;
  write_ref: string;
  /** The container's name — the identity a later leg re-addresses it by. */
  sandbox: string;
  /** The work process inside that container, once the dispatch was confirmed. */
  process_id: string | null;
  /** The epic base the container clones at and collect compares the branch to. */
  base_sha: string;
  /** The branch the container pushes — collect's only evidence. */
  branch: string;
  /** What `start` saw: the container is launched, or why it is not. */
  launched: boolean;
  detail: string;
  /** The dispatch's remaining inputs, so `cancel` can re-derive a boot. */
  run_id: string;
  epic_id: string;
  project: string;
  base_ref: string;
  title: string;
};

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
  /** Boot inputs for one dispatch: repo URL, gateway credential, base SHA, image. */
  boot: SandboxBootSeam;
  /** Spawn knobs (sleep, log sinks, budgets) — the wave machinery's own. */
  spawn?: SpawnOptions;
};

// ----------------------------------------------------------------- start ---

/**
 * The sandbox an adoption question is answered against: the named
 * container, re-addressed on every look. A Workflow replay or a restarted
 * pass cannot carry a live `OrchestratorSandbox` object across the step
 * boundary, so nothing here does.
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
): Promise<SandboxAttemptHandle> {
  const name = attemptSandboxName(spec.run_id, spec.tick_id, spec.attempt);
  const base = {
    executor: SANDBOX_EXECUTOR_NAME,
    job_id: `run-${spec.run_id}/tick-${spec.tick_id}/attempt-${spec.attempt}`,
    attempt: spec.attempt,
    tick_id: spec.tick_id,
    role: spec.role,
    remote: "origin",
    resumed_from: null,
    write_ref: spec.write_ref,
    sandbox: name,
    run_id: spec.run_id,
    epic_id: spec.epic_id,
    project: spec.project,
    base_ref: spec.base_ref,
    title: spec.title,
  } satisfies Omit<
    SandboxAttemptHandle,
    "try" | "process_id" | "base_sha" | "branch" | "launched" | "detail"
  >;

  // Adoption first: a container already holding a live work process is this
  // attempt's, by the name nobody else would boot under, and starting a
  // second one beside it is how a run pays twice for one tick.
  const sandbox = await namedSandbox(deps.binding, name);
  const running = await findWorkProcess(sandbox);
  if (running !== null) {
    const boot = await deps.boot(spec);
    return {
      ...base,
      try: spec.attempt,
      process_id: running.id,
      base_sha: boot.base_sha,
      branch: workerBranch(spec.epic_id, spec.tick_id),
      launched: true,
      detail: "adopted: this container's work process was already running",
    };
  }

  const boot = await deps.boot(spec);
  const work = workerWorkSpec(boot);
  const task = workerTask(spec.epic_id, spec.tick_id, boot.base_sha);
  const spawned = await spawnWorker(deps.binding, name, task, work, deps.spawn);
  return {
    ...base,
    try: spec.attempt,
    process_id: spawned.process_id,
    base_sha: boot.base_sha,
    branch: workerBranch(spec.epic_id, spec.tick_id),
    launched: spawned.launched,
    detail: spawned.detail,
  };
}

// ---------------------------------------------------------------- inspect ---

/**
 * Maps the seam's process state vocabulary onto the status the reconciler
 * settles from: `running` while it works, `exited` with its exit code once
 * terminal, `gone` when nobody can address it at all.
 *
 * Pure, so the mapping is testable without a binding — the vocabulary is
 * the compatibility claim this executor makes ("the same four operations,
 * the same records"), and a mapping exercisable only against a live
 * container is a mapping nobody tests.
 */
export function statusFromProcess(
  view: { state: SandboxProcessState; exit_code: number | null } | null,
): AttemptStatus {
  if (view === null) return { state: "gone" };
  if (view.state === "running") return { state: "running" };
  return { state: "exited", exit_code: view.exit_code };
}

/**
 * Reports what can be SEEN of one attempt, re-addressing the container by
 * name: the work process by id, and — when the id answers nothing — the
 * live process list, because "no id" is an evidence gap and not an absence.
 */
async function inspectAttempt(
  deps: SandboxExecutorDeps,
  handle: SandboxAttemptHandle,
): Promise<AttemptStatus> {
  const sandbox = await namedSandbox(deps.binding, handle.sandbox);
  if (handle.process_id !== null) {
    const view = await sandbox.getProcess(handle.process_id);
    if (view !== null) return statusFromProcess(view);
    // The id answered nothing. The list is the second evidence source, for
    // exactly the id-less case: a container that died and came back, or a
    // supervisor that never recorded the id. Only a list with no work
    // process in it is "gone".
    const running = await findWorkProcess(sandbox);
    if (running !== null) return { state: "running" };
  } else {
    const running = await findWorkProcess(sandbox);
    if (running !== null) return { state: "running" };
  }
  return { state: "gone" };
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
 * Collects one attempt from the durable layer — the branch the container
 * pushed, the report it carries, the boundary it kept — and never from the
 * container itself, which by now may be gone.
 */
async function collectAttempt(
  deps: SandboxExecutorDeps,
  handle: SandboxAttemptHandle,
): Promise<AttemptReport> {
  const report = await deps.collector.collect({
    tick_id: handle.tick_id,
    branch: handle.branch,
    base_sha: handle.base_sha,
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
async function cancelAttempt(deps: SandboxExecutorDeps, handle: SandboxAttemptHandle) {
  const boot = await deps.boot({
    run_id: handle.run_id,
    epic_id: handle.epic_id,
    tick_id: handle.tick_id,
    attempt: handle.attempt,
    role: handle.role,
    project: handle.project,
    write_ref: handle.write_ref,
    base_ref: handle.base_ref,
    title: handle.title,
  });
  const work = workerWorkSpec(boot);
  const salvage: SalvageSpec | undefined = work.salvage;
  await salvageWorker(deps.binding, handle.sandbox, handle.process_id, salvage, {
    reason: "stopped:run",
    ...(deps.spawn?.sleep === undefined ? {} : { sleep: deps.spawn.sleep }),
  });
  await teardownWorker(deps.binding, handle.sandbox, handle.process_id);
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
    async inspect(handle: AttemptHandle): Promise<AttemptStatus> {
      return inspectAttempt(deps, handle as unknown as SandboxAttemptHandle);
    },
    async collect(handle: AttemptHandle): Promise<AttemptReport> {
      return collectAttempt(deps, handle as unknown as SandboxAttemptHandle);
    },
    async cancel(handle: AttemptHandle): Promise<void> {
      await cancelAttempt(deps, handle as unknown as SandboxAttemptHandle);
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
 * (`run-workflow.ts`): a run-scoped gateway token minted per dispatch
 * (rotation is the existing rule), and git access through
 * `planSandboxGit`'s write grade — the repository itself on github.com with
 * the operator's credential, exactly what a write run has always been
 * handed. A deployment missing any piece (the container binding, the
 * factory's own base URL, the epic base) gets no executor and the
 * reconciler's own refusal names it.
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
    boot: async (spec) => {
      // Minted per dispatch: the rotation rule every orchestrator boot
      // already follows — a credential shared across attempts is one
      // revocation cannot take back from just this attempt.
      const credential = await issueRunToken(env, {
        run_id: spec.run_id,
        tick_id: spec.tick_id,
        attempt: spec.attempt,
      });
      return {
        repo_url: git.plan.repo_url,
        base_sha: base,
        epic: spec.epic_id,
        tick: spec.tick_id,
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
