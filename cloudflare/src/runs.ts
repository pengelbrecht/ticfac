/**
 * Run submission, stop and status — the logic behind the authenticated
 * `/api/runs` surface (UC1b, D21, D22).
 *
 * The routing layer in src/index.ts does HTTP; this module does the control
 * plane. Four rules shape it:
 *
 * 1. **The RunRoom is the arbiter, D1 is the index.** Whether a submission may
 *    ignite is decided by the project's RunRoom DO (single-threaded, so three
 *    transports need no cross-transport locking); the `runs` row and the
 *    `dispatch_log` trail are the durable record of what that decision was.
 *    When the two disagree the DO is right — a row is written after the lease
 *    is taken, never before.
 * 2. **A refusal is a logged event, not just a status code.** Every ignition
 *    and every refusal writes `dispatch_log` with a reason from the closed
 *    policy vocabulary (D20), so `tk factory trace` can answer "why did this
 *    not run" from the database rather than from Workers logs.
 * 3. **Enrolment is a security boundary, not bookkeeping.** The bearer token
 *    says "you are this deployment's operator"; it does not say which
 *    repositories the operator pointed their GitHub credential at. A
 *    submission for an unenrolled project is refused before the lease is even
 *    asked for, so the token alone cannot aim the factory at any repo its PAT
 *    can reach (migrations/0003).
 * 4. **Stop is control-plane state.** `stopRun` writes a stop record into the
 *    RunRoom and flips the run's index state; it does not send the
 *    orchestrator a message and does not need it to cooperate (UC1b). The Run
 *    Workflow reads that record and enforces D15's clean stop — finish the
 *    in-flight tick, then review and closeout.
 */

import {
  DEFAULT_RUN_CREDENTIAL_GRADE,
  isRunCredentialGrade,
  RUN_CREDENTIAL_GRADES,
  type RunCredentialGrade,
} from "./credentials";
import {
  type DeploymentImage,
  type DispatchReason,
  deleteRun,
  getDeploymentImage,
  getEnrolledProject,
  getRun,
  getRunImage,
  getRunProgress,
  insertDispatchLog,
  insertRun,
  insertRunImage,
  listRuns,
  type Run,
  type RunProgressRecord,
  updateRunState,
} from "./db";
import type { EpicReconcilerParams } from "./epic-reconciler";
import { modelRoutingComplaint, revokeRunTokens } from "./gateway";
import type { Env } from "./index";
import type {
  DispatchLeaseView,
  LeaseOrigin,
  PendingEntry,
  QueuedSubmission,
  RunRoom,
  StopMode,
  StopRequest,
} from "./run-room";
import { carriedTraceID, parseTraceID } from "./trace";

/**
 * Run lifecycle states written to the `runs` index.
 *
 * `starting` is this module's; everything after it belongs to the Run Workflow
 * (tick ldr), which owns the run once the instance exists. `stopping` is the
 * one state the control plane sets on a live run: it is the durable half of a
 * clean stop, and it is set here rather than by the orchestrator precisely so
 * a wedged orchestrator cannot refuse it.
 */

/**
 * The lease ttl a submission takes, sized to outlive a COLD boot.
 *
 * The lease is acquired here, and the first renewal the Run Workflow makes is
 * the one right after its container is up. Between those two moments sits a
 * whole boot: pulling an image, cloning at the base sha, installing a
 * toolchain, probing the model and the harness, and running pre-flight. The
 * 60s default did not survive that, so a run expired its own lease during boot
 * and read the result as a lost lease — a HARD trip, which revoked its gateway
 * token about a minute in and 403'd the harness on its first real call
 * (tick 4ef; measured on run_d941c5ee).
 *
 * This bounds how long an abandoned pre-boot run wedges the project, so it is
 * generous rather than unbounded. It is only load-bearing until that first
 * post-boot renewal takes over with the poll-sized ttl.
 */
export const BOOT_LEASE_TTL_MS = 600_000;

export const RUN_STATES = [
  "starting",
  "running",
  "stopping",
  "stopped",
  "completed",
  "failed",
] as const;
export type RunState = (typeof RUN_STATES)[number];

/** States in which a run still exists as work: a stop is meaningful here. */
export const ACTIVE_RUN_STATES: readonly RunState[] = ["starting", "running", "stopping"];

/** Runs asked for without a filter. Bounded so `tk cloud status` cannot page a whole history. */
export const DEFAULT_RUN_LIMIT = 50;
export const MAX_RUN_LIMIT = 200;

/**
 * How long a queued submission stays ignitable (D22). A queue that silently
 * ignites work hours later is worse than a refusal, so the window is short by
 * default, per-deployment configurable through the `RUN_QUEUE_TTL_MS` var, and
 * per-submission overridable within the bounds below.
 */
export const DEFAULT_QUEUE_TTL_MS = 1_800_000;

/**
 * Queue window bounds (D22), enforced both here (a fast 400 on a bad
 * submission) and in the RunRoom, which imports them from this module so the
 * two can never drift apart.
 */
export const MIN_QUEUE_TTL_MS = 100;
export const MAX_QUEUE_TTL_MS = 86_400_000;

/** A pushed commit, in full 40-hex form — the submission boundary is a pushed SHA (D3). */
export const BASE_SHA_PATTERN = /^[0-9a-f]{40}$/;

/** A tick id: 3-4 lowercase base36 characters, mirroring `internal/tick.IDGenerator`. */
export const TICK_ID_PATTERN = /^[a-z0-9]{3,4}$/;

/**
 * The canonical `owner/repo` project pair.
 *
 * Remote URLs are deliberately NOT parsed here. `internal/github` already owns
 * that parsing in Go and the CLI resolves the pair before submitting; a second
 * URL parser in TypeScript would be a cross-language format to keep in parity
 * for no gain. An unresolved remote is refused with an actionable message.
 */
const PROJECT_PATTERN = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/;

/** Longest accepted free-text field (epic id, requested_by, notify channel). */
const MAX_FIELD_LENGTH = 200;

// ------------------------------------------------------------- workflow ---

/** Parameters the Run Workflow (tick ldr) boots a run from. */
export type RunWorkflowParams = {
  run_id: string;
  project: string;
  epic: string;
  base_sha: string;
  requested_by: string;
  /**
   * The trace id this run carries into every container it boots (D20, tick
   * hyi). Absent only for an instance created before the field existed — a
   * live Workflow whose params were serialised by an older bundle.
   */
  trace_id?: string;
  notify?: string;
  /**
   * This submission's own budget (tick wn5), bounded by the deployment ceiling
   * when the Workflow builds its config. Absent means "the deployment's".
   */
  max_cost_usd?: number;
  max_wall_clock_ms?: number;
  /**
   * The dispatch lease's release credential. The Workflow renews it while the
   * run lives and releases it at finalize; nobody else ever sees it, which is
   * what compare-and-delete release depends on.
   */
  lease_token: string;
  /**
   * The credential grade the run was submitted with (D11, tick pzf).
   *
   * Carried for the record and for a Workflow that wants it without a read,
   * but it is NOT the authority: every place that decides what a container
   * holds reads `runs.credential_grade` from the index row, because that row
   * is the durable one and a params blob is a copy that a resumed instance
   * could be older than.
   */
  credential_grade?: RunCredentialGrade;
};

export type WorkflowInstanceStatus = { status: string; error?: unknown; output?: unknown };

/** The instance handle this module uses — a structural subset of Cloudflare's. */
export interface RunWorkflowInstance {
  id: string;
  status(): Promise<WorkflowInstanceStatus>;
  sendEvent?(event: { type: string; payload?: unknown }): Promise<void>;
}

/**
 * The Workflows binding this module uses, as a structural subset of
 * `Workflow<RunWorkflowParams>`.
 *
 * Declared structurally rather than imported so the seam is testable (a fake
 * binding can be assigned to `env`) and so this bundle compiles before tick
 * ldr binds the real Workflow in wrangler.toml.
 */
export interface RunWorkflowBinding {
  create(options: { id?: string; params?: RunWorkflowParams }): Promise<RunWorkflowInstance>;
  get(id: string): Promise<RunWorkflowInstance>;
}

/**
 * The Workflows binding, or null when the deployment has none.
 *
 * A factory with no Run Workflow cannot run anything, so submission fails
 * closed (503) rather than recording a run that will never boot — the same
 * "an unprovisioned deployment is a broken deploy" rule auth applies to a
 * missing token secret.
 */
export function runWorkflowBinding(env: Env): RunWorkflowBinding | null {
  const binding = env.RUN_WORKFLOW;
  return binding === undefined || binding === null ? null : binding;
}

// ------------------------------------------------- the reconciler driver ---

/**
 * The EpicReconciler instance handle as the run route uses it — the same
 * structural subset {@link RunWorkflowInstance} is, so one recording fake
 * can stand in for either binding.
 */
export interface EpicReconcilerInstance {
  id: string;
  status(): Promise<WorkflowInstanceStatus>;
  sendEvent?(event: { type: string; payload?: unknown }): Promise<void>;
}

/**
 * The EPIC_RECONCILER binding as the run route uses it (tick nu9): the
 * driver an epic run is handed to, one instance per run keyed by run id
 * (`env.EPIC_RECONCILER.create({ id: run_id, params })`). Declared
 * structurally, like {@link RunWorkflowBinding}, so the seam is testable —
 * a recording fake can be assigned to `env` — and it reads exactly what
 * `EpicReconcilerWorkflow` (src/epic-reconciler.ts) accepts.
 */
export interface EpicReconcilerBinding {
  create(options: { id?: string; params: EpicReconcilerParams }): Promise<EpicReconcilerInstance>;
  get(id: string): Promise<EpicReconcilerInstance>;
}

/**
 * The EpicReconciler Workflow binding, or null when the deployment has none.
 *
 * A factory without it cannot drive an epic run (tick nu9), so an epic
 * submission fails closed (503) rather than recording a run nobody would
 * reconcile — the same rule `runWorkflowBinding`'s 503 has always held,
 * pointed at the binding that actually drives those runs now.
 */
export function epicReconcilerBinding(env: Env): EpicReconcilerBinding | null {
  const binding = env.EPIC_RECONCILER;
  return binding === undefined || binding === null ? null : binding;
}

/**
 * The run branch: the ref the run's `.tick/` records and `.ticfac/` state are
 * pushed to (tick nu9).
 *
 * This is the local reconciler's own convention, verbatim —
 * `internal/reconcile` defaults `IntegrationBranch` to `epic/<epic>` — so a
 * cloud run and a local run of one epic write the same branch, and a person
 * reading either finds the records where the other left them. The submission
 * names an epic, not a branch, because that is the one identity the local
 * host already answers to.
 */
export function epicBranchFor(epic: string): string {
  return `epic/${epic}`;
}

/**
 * Whether EpicReconcilerWorkflow can drive this submission (tick nu9).
 *
 * The reconciler plans an epic's own ticks from its graph, meters no spend,
 * pings no channel when a run ends, and its sandbox executor issues every
 * worker the `write` grade. So the submissions it can honestly take over
 * are exactly the plain epic runs — no budget, no completion ping,
 * no grade the executor would silently upgrade — and everything else keeps
 * the container-agent driver that DOES honour those fields. That is not a
 * temporary shim: xo2's recorded rule is that no execution path is deleted
 * before its ticfac equivalent passes a gate, and the reconciler's gate run
 * (tick u9h) has not run yet. When the Workflow host grows the machinery —
 * budget enforcement, a completion ping, graded credentials — the field
 * moves out of this predicate and the tests that hold it move with it.
 */
export function reconcilerDrives(submission: RunSubmission): boolean {
  return (
    submission.notify === undefined &&
    submission.max_cost_usd === undefined &&
    submission.max_wall_clock_ms === undefined &&
    (submission.credential_grade === undefined ||
      submission.credential_grade === DEFAULT_RUN_CREDENTIAL_GRADE)
  );
}

// ----------------------------------------------------------- submission ---

export type RunSubmission = {
  project: string;
  epic: string;
  base_sha: string;
  requested_by: string;
  /**
   * The identifier joining this run to whatever caused it (D20, tick hyi).
   *
   * Always present after {@link parseSubmission}, which is the OTHER edge
   * beside `submitSignal`: a run submitted straight from `tk cloud run` has no
   * signal behind it and would otherwise be the one thing in the factory
   * nothing can be joined to. A submission that carries one — a dispatched
   * draft does — keeps it, which is what makes the chain survive the days a
   * proposal may sit before a person presses the button.
   */
  trace_id: string;
  notify?: string;
  queue: boolean;
  queue_ttl_ms?: number;
  /** `tk cloud run --max-cost`: this run's cost ceiling, never above the deployment's. */
  max_cost_usd?: number;
  /** `tk cloud run --max-wall-clock`: this run's clock, never above the deployment's. */
  max_wall_clock_ms?: number;
  /**
   * Where the orchestrator that submitted this run sits (D19, tick bmo).
   *
   * The cloud substrate is drivable from anywhere, and a submission from a
   * laptop takes the SAME RunRoom lease a Workflow-hosted run takes —
   * one project, one arbiter, whatever the orchestrator's location. This field
   * is what makes the two distinguishable afterwards, and it matters at
   * exactly the moment someone is refused: "held by run X (local)" tells an
   * operator their own laptop session is in the way, while "(cloud)" tells
   * them the 06:00 sweep is, and those are different next actions.
   *
   * Absent means `cloud`, which is what every submission before this field
   * was.
   */
  origin?: LeaseOrigin;
  /**
   * Which credential this run is issued (D11, tick pzf).
   *
   * Absent means {@link DEFAULT_RUN_CREDENTIAL_GRADE} — `write`, which is what
   * every run before this field was. A weaker grade is asked for; it is never
   * inferred, because inferring it would mean the factory quietly deciding
   * that some run's push should fail.
   */
  credential_grade?: RunCredentialGrade;
};

export type SubmissionParse =
  | { ok: true; submission: RunSubmission }
  | { ok: false; detail: string };

function text(value: unknown, field: string, into: (v: string) => void): string | null {
  if (typeof value !== "string" || value.trim() === "") return `${field} is required`;
  const trimmed = value.trim();
  if (trimmed.length > MAX_FIELD_LENGTH) {
    return `${field} must be at most ${MAX_FIELD_LENGTH} characters`;
  }
  into(trimmed);
  return null;
}

/**
 * Validates a submission body. Returns the reason it cannot be accepted rather
 * than throwing, so the route can answer 400 with something actionable.
 */
export function parseSubmission(body: unknown): SubmissionParse {
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    return { ok: false, detail: "the request body must be a JSON object" };
  }
  const raw = body as Record<string, unknown>;

  let project = "";
  let epic = "";
  let baseSha = "";
  let requestedBy = "";
  const complaint =
    text(raw.project, "project", (v) => (project = v)) ??
    text(raw.epic, "epic", (v) => (epic = v)) ??
    text(raw.base_sha, "base_sha", (v) => (baseSha = v)) ??
    text(raw.requested_by, "requested_by", (v) => (requestedBy = v));
  if (complaint !== null) return { ok: false, detail: complaint };

  project = project.replace(/\.git$/, "");
  if (!PROJECT_PATTERN.test(project)) {
    return {
      ok: false,
      detail: `project must be the canonical owner/repo pair, got "${project}"`,
    };
  }

  if (!BASE_SHA_PATTERN.test(baseSha)) {
    return {
      ok: false,
      detail: "base_sha must be a full 40-character commit sha the epic is pushed at",
    };
  }

  let notify: string | undefined;
  if (raw.notify !== undefined && raw.notify !== null) {
    const bad = text(raw.notify, "notify", (v) => (notify = v));
    if (bad !== null) return { ok: false, detail: bad };
  }

  if (raw.queue !== undefined && typeof raw.queue !== "boolean") {
    return { ok: false, detail: "queue must be a boolean" };
  }

  let queueTtl: number | undefined;
  if (raw.queue_ttl_ms !== undefined && raw.queue_ttl_ms !== null) {
    const value = raw.queue_ttl_ms;
    if (
      !Number.isSafeInteger(value) ||
      (value as number) < MIN_QUEUE_TTL_MS ||
      (value as number) > MAX_QUEUE_TTL_MS
    ) {
      return {
        ok: false,
        detail: `queue_ttl_ms must be an integer between ${MIN_QUEUE_TTL_MS} and ${MAX_QUEUE_TTL_MS} ms, got ${String(value)}`,
      };
    }
    queueTtl = value as number;
  }

  // A budget the factory quietly dropped is worse than one it refused: the
  // operator would believe the run is bounded and it would not be. So an
  // unusable value is a 400, not a fallback to the deployment ceiling.
  let maxCost: number | undefined;
  const costComplaint = budgetField(raw.max_cost_usd, "max_cost_usd", false, (v) => (maxCost = v));
  if (costComplaint !== null) return { ok: false, detail: costComplaint };

  let maxWallClock: number | undefined;
  const clockComplaint = budgetField(
    raw.max_wall_clock_ms,
    "max_wall_clock_ms",
    true,
    (v) => (maxWallClock = v),
  );
  if (clockComplaint !== null) return { ok: false, detail: clockComplaint };

  // The wave field is refused, not ignored (tick l6t): the Run Workflow no
  // longer fans ticks out to worker containers itself — per-tick workers are
  // dispatched by `ticfac run-epic` in the container, through the cloudflare-
  // sandbox executor and the per-tick sandbox door. A submitter still naming
  // a wave (an old CLI, a stale script) must learn that here, as a 400 naming
  // what to do instead, because a silently dropped field is how runs lost
  // waves before — the answer is never to accept it and do something else.
  if (raw.tick_ids !== undefined && raw.tick_ids !== null) {
    return {
      ok: false,
      detail:
        "tick_ids is no longer accepted: the Run Workflow does not dispatch per-tick " +
        "worker containers itself — submit the epic and its container's ticfac run-epic " +
        "dispatches each tick through the per-tick sandbox door",
    };
  }

  // THE OTHER EDGE (D20, tick hyi). A run submitted directly — `tk cloud run`,
  // the 06:00 sweep, an operator's own curl — has no signal behind it, so if
  // nothing minted here it would be the one thing in the factory joinable to
  // nothing. A submission that already carries an id keeps it: `drafts.ts`
  // dispatches an accepted proposal with the id its signal was minted with,
  // days earlier, and re-minting here would sever exactly the join this tick
  // exists to make.
  //
  // A malformed value MINTS rather than refuses, unlike every budget field
  // above. The asymmetry is deliberate and the reason is what each field does:
  // a dropped budget makes an operator believe a run is capped when it is not,
  // while a dropped trace id costs a join. Refusing a run because its
  // diagnostic header was mistyped would be the diagnostic taking down the
  // thing it exists to diagnose.
  const traceID = carriedTraceID(raw.trace_id);
  if (raw.trace_id !== undefined && raw.trace_id !== null && parseTraceID(raw.trace_id) === null) {
    console.error(
      `factory runs: submission for ${project} carried an unusable trace_id ` +
        `${JSON.stringify(raw.trace_id)}; minted ${traceID} instead`,
    );
  }

  // An origin that cannot be parsed is refused rather than defaulted: the
  // value's whole job is to say truthfully who holds the lease, and a
  // submission whose claim about itself was silently rewritten to "cloud"
  // would make a refusal name the wrong kind of orchestrator.
  let origin: LeaseOrigin | undefined;
  if (raw.origin !== undefined && raw.origin !== null) {
    if (raw.origin !== "local" && raw.origin !== "cloud") {
      return {
        ok: false,
        detail: `origin must be "local" or "cloud", got ${JSON.stringify(raw.origin)}`,
      };
    }
    origin = raw.origin;
  }

  // A grade the factory did not understand is a 400, never a fallback to
  // `write`. The two failure modes are not symmetric: a submission that meant
  // read_only and was silently upgraded is a run holding a credential the
  // submitter deliberately withheld from it, which is the whole thing this
  // field exists to prevent.
  let grade: RunCredentialGrade | undefined;
  if (raw.credential_grade !== undefined && raw.credential_grade !== null) {
    if (!isRunCredentialGrade(raw.credential_grade)) {
      return {
        ok: false,
        detail:
          `credential_grade must be one of ${RUN_CREDENTIAL_GRADES.join(", ")}, got ` +
          JSON.stringify(raw.credential_grade),
      };
    }
    grade = raw.credential_grade;
  }

  // The RunRoom's queued-submission record (D22) is shape-frozen; it never
  // carried a wave, and a wave is no longer a thing a submission can ask for
  // at all — see the tick_ids refusal above.

  return {
    ok: true,
    submission: {
      project,
      epic,
      base_sha: baseSha,
      requested_by: requestedBy,
      trace_id: traceID,
      ...(notify === undefined ? {} : { notify }),
      queue: raw.queue === true,
      ...(queueTtl === undefined ? {} : { queue_ttl_ms: queueTtl }),
      ...(maxCost === undefined ? {} : { max_cost_usd: maxCost }),
      ...(maxWallClock === undefined ? {} : { max_wall_clock_ms: maxWallClock }),
      ...(origin === undefined ? {} : { origin }),
      ...(grade === undefined ? {} : { credential_grade: grade }),
    },
  };
}

/**
 * Reads an optional per-run budget. Absent and null both mean "the deployment
 * decides"; anything else must be a positive number of the right kind, because
 * the alternative is a run an operator thinks is capped and is not.
 */
function budgetField(
  value: unknown,
  field: string,
  integer: boolean,
  into: (v: number) => void,
): string | null {
  if (value === undefined || value === null) return null;
  const usable =
    typeof value === "number" && (integer ? Number.isSafeInteger(value) : Number.isFinite(value));
  if (!usable || (value as number) <= 0) {
    return `${field} must be a positive ${integer ? "integer" : "number"}, got ${JSON.stringify(value)}`;
  }
  into(value as number);
  return null;
}

/**
 * The queue window for this deployment: the submission's own value, else the
 * `RUN_QUEUE_TTL_MS` var, else the default. An unusable var is ignored with a
 * log rather than failing a submission — a typo'd window must not take the
 * factory down.
 */
export function queueTtlMs(env: Env, requested?: number): number {
  if (requested !== undefined) return requested;
  const configured = env.RUN_QUEUE_TTL_MS;
  if (typeof configured === "string" && configured.trim() !== "") {
    const parsed = Number(configured);
    if (Number.isSafeInteger(parsed) && parsed >= MIN_QUEUE_TTL_MS && parsed <= MAX_QUEUE_TTL_MS) {
      return parsed;
    }
    console.error(
      `factory runs: RUN_QUEUE_TTL_MS must be an integer between ${MIN_QUEUE_TTL_MS} and ` +
        `${MAX_QUEUE_TTL_MS} ms; ignoring "${configured}" and using ${DEFAULT_QUEUE_TTL_MS}`,
    );
  }
  return DEFAULT_QUEUE_TTL_MS;
}

/** A run id, prefixed so it is identifiable in a log line, an R2 key or a PR body. */
export function newRunID(): string {
  return `run_${crypto.randomUUID().replaceAll("-", "")}`;
}

/** The project's arbiter. One room per project — the lease is per project (D4, D19). */
export function roomFor(env: Env, project: string): DurableObjectStub<RunRoom> {
  return env.RUN_ROOMS.get(env.RUN_ROOMS.idFromName(project));
}

// -------------------------------------------------------------- ignition ---

export type StartRunInput = {
  run_id: string;
  project: string;
  epic: string;
  base_sha: string;
  requested_by: string;
  /** Carried from the submission; see {@link RunSubmission.trace_id}. */
  trace_id?: string;
  notify?: string;
  max_cost_usd?: number;
  max_wall_clock_ms?: number;
  lease_token: string;
  /** See {@link RunSubmission.credential_grade}. Absent means `write`. */
  credential_grade?: RunCredentialGrade;
};

export type StartedRun = { run: Run; workflow: { id: string; status: string } };

/**
 * Records and boots a run whose lease is already held — the half every
 * driver shares (tick nu9).
 *
 * The index row is written before the instance is asked for and UNDONE if
 * the ask fails: a run recorded with no instance behind it would read as
 * live forever, and its id — which the caller may well retry with — would
 * collide on the primary key rather than failing for the reason it actually
 * failed. The stamp and the dispatch log follow, after the instance exists,
 * so an undone ignition leaves nothing behind.
 */
async function bootRun(
  env: Env,
  run: Run,
  boot: () => Promise<{ id: string; status(): Promise<WorkflowInstanceStatus> }>,
): Promise<StartedRun> {
  await insertRun(env.DB, run);
  let instance: { id: string; status(): Promise<WorkflowInstanceStatus> };
  try {
    instance = await boot();
  } catch (error) {
    await deleteRun(env.DB, run.run_id);
    throw error;
  }

  await stampRunImage(env, run.run_id);
  // The stamp is a deployment-level fact recorded PER RUN — one image per
  // container class, chosen at deploy time — because that is the only form
  // that stays true. The next deploy moves the deployment's image; what a
  // finished run booted must not move with it, or the one question the stamp
  // answers ("was my fix even running?") becomes unanswerable the moment it
  // matters.
  await logDispatch(env, {
    run_id: run.run_id,
    epic: run.epic,
    decision: "dispatched",
    reason: null,
  });

  return { run, workflow: { id: instance.id, status: await instanceStatus(instance) } };
}

/** The run row every driver records, built from the shared start fields. */
function runRow(input: {
  run_id: string;
  project: string;
  epic: string;
  base_sha: string;
  requested_by: string;
  trace_id?: string;
  credential_grade?: RunCredentialGrade;
}): Run {
  return {
    run_id: input.run_id,
    project: input.project,
    epic: input.epic,
    base_sha: input.base_sha,
    requested_by: input.requested_by,
    state: "starting",
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    // Written with the row rather than stamped afterwards: the index row is
    // the durable answer to "which run implemented this trace", and a field
    // filled in by a later update is a field a crash between the two leaves
    // empty on exactly the run that needs explaining.
    trace_id: input.trace_id ?? null,
    // Written with the row, like the trace id and for a stronger reason: this
    // is the field the control plane reads back to decide which credential a
    // container is handed, so a run that existed for one instant without it
    // would be a run whose grade a boot could miss (D11, tick pzf).
    credential_grade: input.credential_grade ?? DEFAULT_RUN_CREDENTIAL_GRADE,
  };
}

/**
 * Records and boots a container-agent run whose lease is already held — the
 * Run Workflow driver, kept for every submission the reconciler cannot
 * honestly take over yet (see {@link reconcilerDrives}).
 *
 * Called from the submit route and from the RunRoom's ignite-on-release path,
 * so a queued submission becomes exactly the same run as a direct one. The
 * caller owns the lease: if this throws, it must release it, because a lease
 * held by a run that never booted wedges the project until the ttl expires.
 */
export async function startRun(env: Env, input: StartRunInput): Promise<StartedRun> {
  const workflow = runWorkflowBinding(env);
  if (workflow === null) {
    throw new Error("RUN_WORKFLOW binding is not configured on this deployment");
  }

  const run = runRow(input);

  // The instance id IS the run id: one trace ID threads every layer (D20), and
  // it means status needs no workflow_id column to find the instance again.
  return await bootRun(env, run, async () => {
    const instance = await workflow.create({
      id: run.run_id,
      params: {
        run_id: run.run_id,
        project: run.project,
        epic: run.epic,
        base_sha: run.base_sha,
        requested_by: run.requested_by,
        ...(run.trace_id === null ? {} : { trace_id: run.trace_id }),
        ...(input.notify === undefined ? {} : { notify: input.notify }),
        ...(input.max_cost_usd === undefined ? {} : { max_cost_usd: input.max_cost_usd }),
        ...(input.max_wall_clock_ms === undefined
          ? {}
          : { max_wall_clock_ms: input.max_wall_clock_ms }),
        lease_token: input.lease_token,
        credential_grade: run.credential_grade as RunCredentialGrade,
      },
    });
    return instance;
  });
}

/** The fields the reconciler driver needs — everything else it cannot honour. */
export type StartReconcilerInput = {
  run_id: string;
  project: string;
  epic: string;
  base_sha: string;
  requested_by: string;
  trace_id?: string;
  /** See {@link RunSubmission.credential_grade}. Absent means `write`. */
  credential_grade?: RunCredentialGrade;
  /**
   * The dispatch lease's release credential. The reconciler renews it while
   * the run lives and releases it at the end — the same ownership the Run
   * Workflow's params carried, moved to the driver that now runs the epic.
   */
  lease_token: string;
};

/**
 * Records and boots an EpicReconciler run whose lease is already held (tick
 * nu9) — the driver every plain epic run is handed to, one Workflow instance
 * per run keyed by run id.
 *
 * The Run Workflow stays the driver for the submissions the reconciler
 * cannot honour (see {@link reconcilerDrives}); no new run of THOSE shapes
 * may silently arrive here, which is what `reconcilerDrives`'s tests hold.
 */
export async function startReconcilerRun(
  env: Env,
  input: StartReconcilerInput,
): Promise<StartedRun> {
  const workflow = epicReconcilerBinding(env);
  if (workflow === null) {
    throw new Error("EPIC_RECONCILER binding is not configured on this deployment");
  }

  const run = runRow(input);

  // The instance id IS the run id here too (D20): status and stop find the
  // reconciler's instance the same way they found the agent's.
  return await bootRun(env, run, async () => {
    const instance = await workflow.create({
      id: run.run_id,
      params: {
        run_id: run.run_id,
        epic_id: run.epic,
        project: run.project,
        // The run branch is where the reconciler's durable records live —
        // the local reconciler's own `epic/<epic>` convention, so a cloud run
        // and a local run of one epic write the same branch.
        branch: epicBranchFor(run.epic),
        base_sha: run.base_sha,
        requested_by: run.requested_by,
        // The lease's release credential rides to the driver, which renews it
        // every pass and releases it at the end — the Run Workflow's own
        // ownership, on the driver that owns the run now.
        lease_token: input.lease_token,
      },
    });
    return instance;
  });
}

/**
 * Boots a queued submission's run on the driver its parked fields name (tick
 * nu9): the RunRoom's ignite-on-release path calls this with the parked
 * record, and the record's own shape decides the driver — the same rule
 * {@link reconcilerDrives} holds the live route to, read off the columns a
 * parked submission kept. A queued submission can carry a budget or a
 * completion ping or a held-back grade, and those are the agent's still; a
 * plain parked epic run ignites on the reconciler.
 */
export async function igniteRun(env: Env, input: StartRunInput): Promise<StartedRun> {
  const reconcilerShaped =
    input.notify === undefined &&
    input.max_cost_usd === undefined &&
    input.max_wall_clock_ms === undefined &&
    (input.credential_grade === undefined ||
      input.credential_grade === DEFAULT_RUN_CREDENTIAL_GRADE);
  if (reconcilerShaped) {
    return await startReconcilerRun(env, input);
  }
  return await startRun(env, input);
}

/**
 * Records which orchestrator image the run boots, best effort.
 *
 * Never fatal: the stamp is observability, and a run that ignited must not be
 * undone because a bookkeeping row could not be written. A factory deployed
 * before the rollout wait existed simply has nothing to stamp.
 */
async function stampRunImage(env: Env, runID: string): Promise<void> {
  try {
    const image = await getDeploymentImage(env.DB);
    if (image === null) return;
    await insertRunImage(env.DB, runID, image, new Date().toISOString());
  } catch (error) {
    console.error(`factory runs: recording the image for run ${runID} failed: ${String(error)}`);
  }
}

async function instanceStatus(instance: {
  id: string;
  status(): Promise<WorkflowInstanceStatus>;
}): Promise<string> {
  try {
    return (await instance.status()).status;
  } catch (error) {
    // An instance that cannot report is not a run that failed: status is
    // observability, and a broken read must not turn a live run into an error.
    console.error(`factory runs: workflow instance ${instance.id} status failed: ${String(error)}`);
    return "unknown";
  }
}

/**
 * Writes one dispatch decision.
 *
 * `tick_id` is the epic: a run-level decision is about the epic tick that was
 * submitted, which is what makes the trail joinable to the tracker. The reason
 * vocabulary is closed by the D1 CHECK constraint (see src/db.ts), so the
 * holder's run id travels in `decision` — `lease_held_by:<run>` as the design's
 * dispatch taxonomy writes it — rather than being lost to a log line.
 */
export async function logDispatch(
  env: Env,
  entry: {
    run_id: string;
    epic: string;
    decision: string;
    /** From the closed policy vocabulary the D1 CHECK constraint enforces. */
    reason: DispatchReason | null;
  },
): Promise<void> {
  await insertDispatchLog(env.DB, {
    run_id: entry.run_id,
    tick_id: entry.epic,
    decision: entry.decision,
    reason: entry.reason,
    at: new Date().toISOString(),
  });
}

export type SubmitResult =
  | { outcome: "started"; started: StartedRun }
  | { outcome: "not_enrolled"; detail: string; run_id: string }
  | { outcome: "queued"; queued: QueuedSubmission; holder: DispatchLeaseView }
  | {
      outcome: "refused";
      reason: string;
      holder: DispatchLeaseView;
      detail: string;
      run_id: string;
    }
  | { outcome: "invalid"; detail: string }
  | { outcome: "unavailable"; detail: string };

/**
 * The submit path: lease, then ignite — or refuse, or park.
 *
 * The lease is asked for before anything durable is written, so a refused
 * submission leaves no half-run behind; only the dispatch_log entry records
 * that it happened at all.
 *
 * Which driver the submission rides is decided here (tick nu9): the
 * reconciler for a plain epic run, the container agent for everything it
 * cannot honour. `opts.driver: "agent"` is the one explicit opt-out — for a
 * submitter whose ask is narrower than the reconciler's plan (a draft press
 * runs its tick now) — and it is a named decision rather than a shape a
 * future rule might silently re-route.
 */
export async function submitRun(
  env: Env,
  submission: RunSubmission,
  opts?: { driver?: "agent" },
): Promise<SubmitResult> {
  // Which driver this submission can honestly ride (tick nu9): the
  // reconciler for a plain epic run, the container agent for everything it
  // cannot honour — the budgets, the completion ping, the grades the
  // executor would upgrade. The route, the sweeps, the drafts, the
  // reviews and the remediations all submit through this one choke point,
  // so the predicate is the one place the split lives and its tests are the
  // contract every submitter is held to.
  const drives = opts?.driver !== "agent" && reconcilerDrives(submission);
  if (drives) {
    if (epicReconcilerBinding(env) === null) {
      // Fail closed and say which binding: a run recorded now would never be
      // reconciled. The reconciler drives every plain epic run (tick nu9), so
      // this is the availability answer for the whole epic-run route.
      console.error(
        "factory runs: EPIC_RECONCILER binding is missing; refusing every epic submission",
      );
      return {
        outcome: "unavailable",
        detail: "EPIC_RECONCILER binding is not configured on this deployment",
      };
    }
  } else if (runWorkflowBinding(env) === null) {
    // Fail closed and say which binding: a run recorded now would never boot.
    console.error("factory runs: RUN_WORKFLOW binding is missing; refusing every submission");
    return {
      outcome: "unavailable",
      detail: "RUN_WORKFLOW binding is not configured on this deployment",
    };
  }

  // All cloud model traffic goes through the operator's own AI Gateway (D17).
  // A deployment with none configured must say so HERE, at submission, while
  // there is still an operator reading the answer — never by booting a sandbox
  // that quietly falls back to a vendor default, and never by failing a run
  // minutes later with a message nobody is watching for.
  const routing = modelRoutingComplaint(env);
  if (routing !== null) {
    console.error(`factory runs: refusing every submission — ${routing}`);
    return { outcome: "unavailable", detail: routing };
  }

  const runID = newRunID();

  // Enrolment first: a project this factory was never pointed at must not even
  // reach the arbiter, and the refusal is a dispatch decision like any other.
  if ((await getEnrolledProject(env.DB, submission.project)) === null) {
    await logDispatch(env, {
      run_id: runID,
      epic: submission.epic,
      decision: `refused:not_enrolled:${submission.project}`,
      reason: "awaiting_approval",
    });
    return {
      outcome: "not_enrolled",
      run_id: runID,
      detail:
        `project ${submission.project} is not enrolled with this factory. ` +
        `Enrol it with POST /api/projects {"project":"${submission.project}"} ` +
        `before submitting a run.`,
    };
  }

  const room = roomFor(env, submission.project);
  const lease = await room.acquireDispatchLease({
    run_id: runID,
    epic: submission.epic,
    // The lease is the same lease wherever the orchestrator sits (D19); the
    // origin only records which side asked for it.
    origin: submission.origin ?? "cloud",
    requested_by: submission.requested_by,
    ttl_ms: BOOT_LEASE_TTL_MS,
  });

  if (lease.ok) {
    try {
      return {
        outcome: "started",
        started: drives
          ? await startReconcilerRun(env, {
              run_id: runID,
              project: submission.project,
              epic: submission.epic,
              base_sha: submission.base_sha,
              requested_by: submission.requested_by,
              trace_id: submission.trace_id,
              ...(submission.credential_grade === undefined
                ? {}
                : { credential_grade: submission.credential_grade }),
              lease_token: lease.lease.token,
            })
          : await startRun(env, {
              run_id: runID,
              project: submission.project,
              epic: submission.epic,
              base_sha: submission.base_sha,
              requested_by: submission.requested_by,
              trace_id: submission.trace_id,
              ...(submission.notify === undefined ? {} : { notify: submission.notify }),
              ...(submission.max_cost_usd === undefined
                ? {}
                : { max_cost_usd: submission.max_cost_usd }),
              ...(submission.max_wall_clock_ms === undefined
                ? {}
                : { max_wall_clock_ms: submission.max_wall_clock_ms }),
              lease_token: lease.lease.token,
              ...(submission.credential_grade === undefined
                ? {}
                : { credential_grade: submission.credential_grade }),
            }),
      };
    } catch (error) {
      // Hand the project back rather than wedging it for a lease ttl on a
      // failure that has nothing to do with contention.
      await room.releaseDispatchLease({ run_id: runID, token: lease.lease.token });
      console.error(`factory runs: ignition failed for ${runID}: ${String(error)}`);
      return { outcome: "unavailable", detail: `run ${runID} could not be started` };
    }
  }

  if (lease.error === "invalid_request") return { outcome: "invalid", detail: lease.detail };

  // Refused. Both branches below are dispatch decisions and both are logged:
  // "why did this not run" must be answerable from D1 (D20).
  if (!submission.queue) {
    await logDispatch(env, {
      run_id: runID,
      epic: submission.epic,
      decision: `refused:${lease.reason}`,
      reason: "lease_held_by",
    });
    return {
      outcome: "refused",
      reason: lease.reason,
      holder: lease.holder,
      detail: lease.detail,
      run_id: runID,
    };
  }

  const parked = await room.queueSubmission({
    run_id: runID,
    project: submission.project,
    epic: submission.epic,
    base_sha: submission.base_sha,
    requested_by: submission.requested_by,
    // Parked WITH the submission, for the same reason the draft row carries
    // one: a queued run ignites later, from the RunRoom's alarm rather than
    // from this request, and a trace id left on the stack here would not
    // survive that gap.
    trace_id: submission.trace_id,
    ...(submission.notify === undefined ? {} : { notify: submission.notify }),
    ...(submission.max_cost_usd === undefined ? {} : { max_cost_usd: submission.max_cost_usd }),
    ...(submission.max_wall_clock_ms === undefined
      ? {}
      : { max_wall_clock_ms: submission.max_wall_clock_ms }),
    // Parked for the same reason the budget is (tick wn5), and with a sharper
    // edge: a queued read-only submission that ignited as `write` would be the
    // factory silently granting a run the push access its submitter withheld.
    ...(submission.credential_grade === undefined
      ? {}
      : { credential_grade: submission.credential_grade }),
    blocked_by: lease.holder.run_id,
    ttl_ms: queueTtlMs(env, submission.queue_ttl_ms),
  });
  if (parked.ok === false && parked.error === "invalid_request") {
    return { outcome: "invalid", detail: parked.detail };
  }

  // A repeat park for the same epic comes back as the entry that already
  // stands (`already_queued`), which is the same answer the operator wants.
  const queued = parked.queued;
  await logDispatch(env, {
    run_id: queued.run_id,
    epic: submission.epic,
    decision: `queued:${lease.reason}`,
    reason: "lease_held_by",
  });
  return { outcome: "queued", queued, holder: lease.holder };
}

// ------------------------------------------------------------------ stop ---

/**
 * Delivers the stop event to whichever Workflow instance drives the run —
 * best effort, never load-bearing (the RunRoom's stop record is what makes a
 * stop true; the driver enforces it at its next step boundary).
 *
 * The reconciler binding is asked first because every plain epic run lives
 * there (tick nu9); the Run Workflow is still asked after it for the runs the
 * reconciler cannot take over — budgeted runs, reviews — so neither
 * driver's live runs are silently unreachable. A binding with no instance
 * for the id, or an instance not waiting on an event, answers nothing and
 * the next binding is tried — the same graceful shape the single-binding
 * version had.
 */
async function deliverStopEvent(env: Env, runID: string, stop: StopRequest): Promise<boolean> {
  const bindings: Array<EpicReconcilerBinding | RunWorkflowBinding | null> = [
    epicReconcilerBinding(env),
    runWorkflowBinding(env),
  ];
  for (const workflow of bindings) {
    if (workflow === null) continue;
    try {
      const instance = await workflow.get(runID);
      if (typeof instance.sendEvent !== "function") continue;
      await instance.sendEvent({ type: "stop", payload: stop });
      return true;
    } catch (error) {
      // Expected whenever the instance is not waiting on an event. The stop is
      // already durable; the driver reads it at its next step boundary.
      console.error(
        `factory runs: could not deliver the stop event to workflow ${runID} ` +
          `(the stop record stands): ${String(error)}`,
      );
    }
  }
  return false;
}

export type StopResult =
  | {
      outcome: "stopping";
      run: Run;
      stop: StopRequest;
      already: boolean;
      workflow_notified: boolean;
      /** What was actually performed, so an operator is never left guessing. */
      mode: StopMode;
      /** Live gateway credentials this stop killed. Always 0 for a clean stop. */
      tokens_revoked: number;
      /**
       * Set when the run's supervisor had ALREADY ENDED (errored, terminated
       * or complete) when the stop arrived: there was nobody left to honour
       * it, so this request finished the stop itself and `run.state` is
       * `stopped`. The value is the supervisor's status.
       */
      supervisor_ended?: string;
    }
  | { outcome: "unknown_run" }
  | { outcome: "not_active"; run: Run }
  | { outcome: "invalid"; detail: string };

/**
 * A stop (D15 semantics), enforced at the control plane.
 *
 * The stop record lands in the project's RunRoom — the arbiter the Run
 * Workflow reads at every step boundary — and the run's index state flips to
 * `stopping`. The orchestrator is never asked: a wedged or adversarial one
 * would not honour a message it never reads, so the Workflow enforces this at
 * its own layer. The `sendEvent` below is an optimisation that lets a waiting
 * Workflow react immediately; the record is what makes the stop true, so a
 * failure to deliver it is reported, not fatal.
 *
 * A `hard` stop revokes the run's gateway credentials HERE, in the request
 * that asked for it, before anything downstream is told anything (tick gyl).
 * The clean path revokes at the end of the grace window, which is right for an
 * ordinary stop and wrong for a runaway: a live run at twice its budget kept
 * spending through a clean stop, and through a hand-written revocation the
 * next boot undid, until its container application was deleted. The
 * credential dying first is what makes the rest of the unwind safe to take its
 * time.
 */
export async function stopRun(
  env: Env,
  runID: string,
  requestedBy: string,
  mode: StopMode = "clean",
): Promise<StopResult> {
  if (typeof runID !== "string" || runID.trim() === "") {
    return { outcome: "invalid", detail: "run id is required" };
  }
  if (mode !== "clean" && mode !== "hard") {
    return { outcome: "invalid", detail: `stop mode must be "clean" or "hard"` };
  }

  const run = await getRun(env.DB, runID);
  if (run === null) return { outcome: "unknown_run" };
  if (!ACTIVE_RUN_STATES.includes(run.state as RunState)) {
    return { outcome: "not_active", run };
  }

  const room = roomFor(env, run.project);
  const requested = await room.requestStop({
    run_id: runID,
    requested_by: requestedBy,
    mode,
  });
  if (requested.ok === false) return { outcome: "invalid", detail: requested.detail };

  // Before the state flip, before the Workflow is told, before anything can
  // observe a stop and take its time about it: the credential is the kill
  // switch (D17), so on a hard stop it dies in this request. The record above
  // is what keeps it dead — no later boot of this run may mint another.
  const tokensRevoked =
    requested.stop.mode === "hard"
      ? await revokeRunTokens(env, runID, `stopped:hard:${requestedBy}`).catch((error: unknown) => {
          console.error(
            `factory runs: ${runID} could not revoke its gateway tokens on a hard stop: ${String(error)}`,
          );
          return 0;
        })
      : 0;

  const updated = (await updateRunState(env.DB, runID, "stopping")) ?? {
    ...run,
    state: "stopping",
  };

  const notified = await deliverStopEvent(env, runID, requested.stop);

  // A stop is HONOURED by the run's own supervisor: the flip above says
  // `stopping`, and the supervisor's finalize is what makes it `stopped`. A
  // supervisor that has already ended — errored (a boot that could not get a
  // container: "Maximum number of running container instances exceeded"),
  // terminated, or complete — will never read the stop, so the record froze
  // at `stopping` and stayed there, counted as ACTIVE, until somebody noticed.
  // Four runs were found that way on 2026-09-23. Nothing is left to hand the
  // stop to, so it is finished here: the credential dies (a clean stop's
  // revocation would have been the supervisor's) and the row goes terminal.
  // Only a CONFIRMED ended status does this; a supervisor that cannot be read
  // is left alone, because finishing a live run's record on a failed read
  // would be worse than a stuck one.
  const phase = await endedSupervisor(env, updated);
  if (phase !== null) {
    const revokedHere =
      requested.stop.mode === "hard"
        ? 0
        : await revokeRunTokens(
            env,
            runID,
            `stopped:supervisor-${phase.status}:${requestedBy}`,
          ).catch((error: unknown) => {
            console.error(
              `factory runs: ${runID} could not revoke its gateway tokens finishing a stop: ${String(error)}`,
            );
            return 0;
          });
    const finished = (await updateRunState(env.DB, runID, "stopped", new Date().toISOString())) ?? {
      ...updated,
      state: "stopped",
    };
    return {
      outcome: "stopping",
      run: finished,
      stop: requested.stop,
      already: requested.already,
      workflow_notified: notified,
      mode: requested.stop.mode,
      tokens_revoked: tokensRevoked + revokedHere,
      supervisor_ended: phase.status,
    };
  }

  return {
    outcome: "stopping",
    run: updated,
    stop: requested.stop,
    already: requested.already,
    workflow_notified: notified,
    mode: requested.stop.mode,
    tokens_revoked: tokensRevoked,
  };
}

// ---------------------------------------------------------------- status ---

export type RunStatus = {
  run: Run;
  phase: { state: string; workflow: { id: string; status: string } | null };
  lease: DispatchLeaseView | null;
  queued: QueuedSubmission[];
  gates: PendingEntry[];
  stop: StopRequest | null;
  /**
   * The orchestrator container image this run booted, or null for a run that
   * started before any deploy confirmed one. It is here because the image is
   * the difference between "the fix did not work" and "the fix was never
   * running", and that difference is invisible from the run's output alone.
   */
  image: DeploymentImage | null;
  /**
   * What the durable layer said when the run ended, or null for a live run (or
   * one that predates the stamp).
   *
   * Reported beside the state because the state alone cannot answer the
   * question an operator actually has after a run: did anything happen? A
   * `stopped` run that pushed a wave and a `stopped` run that printed a
   * paragraph and exited read identically without it — and it was that second
   * shape being called `completed` that this exists to make visible (tick ehy).
   */
  progress: RunProgressRecord | null;
};

/** Everything `tk cloud status <run>` shows: index row, Workflow step state, lease, gates, queue. */
export async function runStatus(env: Env, runID: string): Promise<RunStatus | null> {
  const run = await getRun(env.DB, runID);
  if (run === null) return null;

  const room = roomFor(env, run.project);
  const [lease, queued, gates, stop, image, progress] = await Promise.all([
    room.leaseStatus(),
    room.listQueuedSubmissions(),
    room.listQuestions(),
    room.stopRequest(runID),
    getRunImage(env.DB, runID),
    getRunProgress(env.DB, runID),
  ]);

  return {
    run,
    phase: { state: run.state, workflow: await workflowPhase(env, run) },
    lease,
    queued,
    gates,
    stop,
    image,
    progress,
  };
}

/** Workflow instance statuses after which nothing will ever read a stop. */
const SUPERVISOR_ENDED: ReadonlySet<string> = new Set(["errored", "terminated", "complete"]);

/**
 * The run's supervisor, when it has CERTAINLY ended; null otherwise.
 *
 * A run's instance lives on ONE of the two Workflow bindings, and asking the
 * other one does not reliably throw — it can hand back an instance that
 * reports an ended status for an id it never ran. So the first answer is not
 * the answer (which is how the first cut of this finished LIVE runs' stops):
 * every binding is asked, and the supervisor counts as ended only when at
 * least one instance was found and NONE of them reports anything but an ended
 * status. Any live-looking answer — running, queued, waiting, paused, or a
 * status that could not be read — leaves the stop to the supervisor.
 */
async function endedSupervisor(env: Env, run: Run): Promise<{ id: string; status: string } | null> {
  let ended: { id: string; status: string } | null = null;
  for (const workflow of [epicReconcilerBinding(env), runWorkflowBinding(env)]) {
    if (workflow === null) continue;
    let instance: Awaited<ReturnType<typeof workflow.get>>;
    try {
      instance = await workflow.get(run.run_id);
    } catch {
      continue;
    }
    const status = await instanceStatus(instance);
    if (!SUPERVISOR_ENDED.has(status)) return null;
    ended ??= { id: instance.id, status };
  }
  return ended;
}

async function workflowPhase(env: Env, run: Run): Promise<{ id: string; status: string } | null> {
  // The reconciler first — every plain epic run's instance lives there (tick
  // nu9) — then the Run Workflow, for the runs it still drives: budgeted
  // runs, reviews.
  for (const workflow of [epicReconcilerBinding(env), runWorkflowBinding(env)]) {
    if (workflow === null) continue;
    try {
      const instance = await workflow.get(run.run_id);
      return { id: instance.id, status: await instanceStatus(instance) };
    } catch (error) {
      // A run whose instance is not on this binding: try the next one. A run
      // with an instance nowhere (retention, or never created) still has an
      // index row and a lease worth reporting.
      console.error(
        `factory runs: workflow instance ${run.run_id} is unavailable: ${String(error)}`,
      );
    }
  }
  return null;
}

export type ProjectStatus = {
  project: string;
  lease: DispatchLeaseView | null;
  queued: QueuedSubmission[];
};

export type RunListing = { runs: Run[]; projects: ProjectStatus[] };

/**
 * The run list plus per-project arbiter state.
 *
 * Queued submissions live in the DO, not in `runs` — they are not runs yet —
 * so a listing that only read D1 would answer "nothing is happening" while a
 * parked submission waits. Every project the listing mentions is asked.
 */
export async function listRunStatus(
  env: Env,
  filter: { project?: string; state?: string; limit?: number },
): Promise<RunListing> {
  const runs = await listRuns(env.DB, filter);

  const projects = new Set(runs.map((run) => run.project));
  if (filter.project !== undefined) projects.add(filter.project);

  const statuses = await Promise.all(
    [...projects].sort().map(async (project): Promise<ProjectStatus> => {
      const room = roomFor(env, project);
      const [lease, queued] = await Promise.all([room.leaseStatus(), room.listQueuedSubmissions()]);
      return { project, lease, queued };
    }),
  );

  return { runs, projects: statuses };
}
