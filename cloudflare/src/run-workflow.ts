/**
 * The Run Workflow — the thing that makes a run survive everything below it.
 *
 * A run is a supervisor and a container, and only one of them is durable. The
 * container is *expected* to die: context exhaustion, container eviction, an
 * API that stops answering. The supervisor cannot be lost, which is why it is a
 * Cloudflare Workflow: every step is checkpointed, so a run outlives the Worker
 * isolate, the sandbox, and the region.
 *
 * Five rules shape this module.
 *
 * 1. **The sandbox is disposable, the run is not.** A dead orchestrator is a
 *    reboot, not a failure. The fresh sandbox's first instruction is the
 *    reconcile protocol (`TICKS_PHASE=reconcile`), which adopts pushed state
 *    instead of redoing merged work — the same protocol a local `tk herd
 *    reconcile` runs, and the same one an operator's stop-edit-restart rides.
 *    Boots are bounded: a container that dies three times is telling you
 *    something a fourth boot will not fix.
 * 2. **Budgets are enforced HERE, never in a prompt.** A model can be talked
 *    out of a budget; a Workflow step cannot (D14). Wall-clock and cost are
 *    checked at every observation, against ground truth — the elapsed time this
 *    module measured and the `cost_usd` this module reads back from AI Gateway
 *    logs — never against anything the agent reports. And enforcement does not
 *    stop at killing a process: a trip revokes the run's gateway token, so an
 *    orchestrator that survives its own kill still cannot spend (D17).
 * 3. **Exhaustion is a clean stop, identical to the operator stop path
 *    (D15).** Both trip the same branch: revoke per the tick-gyl ordering,
 *    give the in-flight work a bounded grace window, drain and kill the
 *    container — and END the run. Since tick dl8 no second container is
 *    booted on a trip: the one orchestrator runs `ticfac run-epic`, which
 *    commits and pushes as it goes, so the branch IS the run's state and a
 *    new run adopts it (`reconcile`); a `closeout` boot would only have
 *    re-run the epic. There is no "abandon the run" outcome either way:
 *    what the run did is on origin, review and close-out are role jobs the
 *    one orchestrator runs inside the run, and a stop that lands before them
 *    leaves the epic resumable by the next run.
 * 4. **Harness output streams to R2 during the run, never at exit (D20).** The
 *    crashed run is exactly the run whose logs you need, so every observation
 *    flushes what the orchestrator has printed since the last one. Reading the
 *    stream mid-run is a supported operation, not a debugging accident.
 * 5. **Finalize always runs.** Whatever happened — completed, stopped, failed,
 *    unprovisioned — the lease is released, the index row reaches a terminal
 *    state, and `run.json` says why. A run that ends without releasing its
 *    lease wedges the project until the lease ttl expires.
 * 6. **Completion is proved, never inferred from an exit status (tick ehy).**
 *    A harness exits 0 when it has nothing left to say, which is not the same
 *    as having done something: the first run whose boot chain fully succeeded
 *    printed 271 bytes, dispatched nothing, pushed no branch, left the epic's
 *    ticks open — and was recorded COMPLETED and charged for. So the exit
 *    status only decides whether to reboot; whether the epic MOVED is decided
 *    against the durable layer (src/progress.ts: the remote's refs, before and
 *    after). A run that stopped without advancing anything is `stopped`, and
 *    `completed` means the epic actually moved.
 * 8. **The completion signal wakes the watch; the branch decides (tick
 *    7eq).** A finished orchestrator POSTs the factory's done door, the
 *    Worker turns that into `instance.sendEvent()`, and the next wait
 *    returns immediately — no polling for a finish that has already
 *    happened. The event is buffered by the platform, so a container that
 *    finished before its supervisor resumed loses nothing; and it decides
 *    nothing, because the container may still die after finishing and
 *    before its callback lands. The looks still read the process and the
 *    budgets, and the verdict still comes from the durable layer. The event
 *    is the optimisation; the pushed branch is the truth.
 *
 * See docs/design/cloud-factory.md (Phase 1, UC1, UC1b, D14, D15, D19, D20).
 */

import { WorkflowEntrypoint, type WorkflowEvent, type WorkflowStep } from "cloudflare:workers";

import {
  type RunRecord,
  writeCombinedHarnessLog,
  writeHarnessSegment,
  writeReconcileRecord,
  writeRunRecord,
} from "./artifacts";
import {
  containerGitToken,
  credentialGrade,
  planSandboxGit,
  type SandboxGitPlan,
} from "./credentials";
import { getRun, recordRunProgress, updateRunState } from "./db";
import {
  factoryBaseURL,
  issueRunToken,
  modelRoutingComplaint,
  revokeRunTokens,
  runGatewayEndpoint,
  spendFailureRemedy,
  syncRunCost,
} from "./gateway";
import type { Env } from "./index";
import {
  getReviewForRun,
  type ReviewTarget,
  reviewEvidence,
  reviewGradeComplaint,
} from "./pr-review";
import {
  compareSnapshots,
  type RefSnapshot,
  type RunProgress,
  snapshotRefs,
  unverifiedProgress,
} from "./progress";
import { readDeclaredSandboxImage } from "./repo-config";
import { DONE_EVENT_TYPE, type DoneSignal, readDoneSignal } from "./run-done";
import { epicCompleted, epicStarted, publishRunEvents } from "./run-events";
import {
  appendFeed,
  FINAL_FEED_SEQ,
  runFinishedFeedEvent,
  runStartedFeedEvent,
  START_FEED_SEQ,
} from "./run-feed";
import { DEFAULT_LEASE_TTL_MS, type LeaseLostReason, MAX_LEASE_TTL_MS } from "./run-room";
import { logDispatch, type RunWorkflowParams, roomFor } from "./runs";
import {
  deploymentImage,
  isTerminalExit,
  ORCHESTRATOR_COMMAND,
  type OrchestratorPhase,
  orchestratorEnv,
  repoURL,
  resolveSandboxImage,
  type SandboxProcessState,
  sandboxBinding,
  sandboxName,
  terminalExitReason,
} from "./sandbox";
import { workerHarness, workerModel } from "./worker-boot";

// ------------------------------------------------------------- the shape ---

/**
 * How many orchestrator sandboxes one run may burn through.
 *
 * Bounded on purpose. A container that cannot stay alive is usually a broken
 * environment, and an unbounded reboot loop spends real money reaching the same
 * answer with a bigger bill.
 */
export const MAX_SANDBOX_BOOTS = 3;

/*
 * The closeout pass that used to follow a trip had its own, smaller boot
 * allowance (MAX_CLOSEOUT_BOOTS) and its own window (RUN_CLOSEOUT_MS). Tick dl8
 * deleted that pass: since hn0 the container execs `ticfac run-epic`, which
 * ignores TICKS_PHASE and TICKS_STOP_REASON, so a closeout boot re-ran the
 * epic instead of winding it down — an agent-orchestrator path that no
 * orchestrator exists to serve. A tripped run now ends (revoke, grace, drain,
 * kill) and no second container is booted, so both constants are gone. The
 * names live on here only until the pinned lifecycle-invariants bundle names
 * them out (A11 cross-references MAX_CLOSEOUT_BOOTS; a re-cut is filed).
 */

/**
 * Observations per boot.
 *
 * Cloudflare caps a Workflow instance's step count, and each observation costs
 * two steps (a sleep and a check). This bound, times the boot allowances above,
 * is what keeps a long run inside that cap — see `pollDelay` for how the
 * interval stretches to cover a long run within a fixed number of looks.
 *
 * Running out of looks is NOT a dead orchestrator. It is a run that outlived
 * what this Workflow instance can watch, and it takes the clean-stop path: the
 * one thing it must never do is boot a second orchestrator alongside a healthy
 * one, which would put two writers on the same `.tick/` (D4).
 */
export const MAX_OBSERVATIONS = 80;

/** Defaults for the deployment-configurable budgets. */
export const DEFAULT_MAX_WALL_CLOCK_MS = 21_600_000; // 6 hours
export const DEFAULT_MAX_COST_USD = 25;
/** How long the in-flight work has to land before a clean stop kills it. */
export const DEFAULT_STOP_GRACE_MS = 300_000; // 5 minutes

/**
 * Observation cadence. Fast at first — a broken boot, a missing toolchain and
 * the first harness output all happen early — then backing off to a cadence a
 * multi-hour run can afford within `MAX_OBSERVATIONS` looks.
 */
export const MIN_POLL_MS = 15_000;
export const MAX_POLL_MS = 300_000;
const POLL_BACKOFF = 1.25;

/**
 * How much of the remaining cost headroom one sleep may be projected to spend.
 *
 * `detectTrip` only runs on an observation, so whatever a run burns during a
 * sleep is spent unwatched — the backoff above widens that window to five
 * minutes exactly when a run is long-lived and expensive, which is when it
 * matters. Measured on run_62c289d1: `RUN_MAX_COST_USD` was 5, `syncRunCost`
 * recorded 5.86 correctly, and the run was still `running` with no trip. The
 * accounting was right; the cadence was not.
 *
 * So the sleep before each look is capped at the time the *observed* burn rate
 * says it would take to spend this fraction of what is left. The remaining
 * headroom halves each look as the ceiling nears, until the cap reaches
 * `MIN_POLL_MS` and the overshoot is bounded by one fast poll's worth of spend
 * rather than one backed-off poll's worth. It costs a handful of extra looks,
 * and only in the last stretch before a trip.
 *
 * The wall clock rides the same cadence and so overshot the same way — on
 * run a1f87597 a 45-minute ceiling was crossed at 11:29:52 and the token was
 * revoked at 11:31:59, two minutes late because the crossing happened partway
 * through a sleep. That one needs no projection: see `deadlineCap`.
 */
export const BUDGET_POLL_HEADROOM = 0.5;

/** Retry policies. Named so the intent survives the config literal. */
const CONTEXT_RETRIES = { retries: { limit: 3, delay: 1_000, backoff: "exponential" } } as const;
const BOOT_RETRIES = { retries: { limit: 2, delay: 2_000, backoff: "exponential" } } as const;
const OBSERVE_RETRIES = { retries: { limit: 3, delay: 500, backoff: "constant" } } as const;
const FINALIZE_RETRIES = { retries: { limit: 5, delay: 1_000, backoff: "exponential" } } as const;

/**
 * How long the boot step — the one step that starts paid work — may run (cr4).
 *
 * A config-less step inherits not one default but two: ten minutes of
 * timeout and FIVE retries ten seconds apart, exponentially. The retry half
 * is the dangerous one and is fixed by carrying a deliberate policy (the
 * `BOOT_RETRIES` this step already had); the timeout half is fixed here, by
 * sizing the step for what it actually does. Starting a process in a fresh
 * container is a cold image pull and a spawn — minutes at the outside, never
 * ten — and a step that hangs must fail inside this window so the run can
 * end (or re-boot) rather than holding an orchestrator nobody can reach.
 */
const BOOT_STEP_TIMEOUT_MS = 300_000;

/**
 * The floor on a `step.waitForEvent` timeout (ticks 7eq, cr4).
 *
 * The platform's event timeouts are documented as settable between one second
 * and 365 days — below the floor is a value the deployed platform may refuse
 * or clamp, and a request-shape error is never a place to learn the platform's
 * real bound (`.tick/learnings.md`: a bodyless 4xx is REQUEST SHAPE until
 * proven otherwise). The supervisor's look cadence is slower than this in
 * every real deployment (the default backoff starts at fifteen seconds), so
 * the floor only bites a test configuration — which takes the plain `sleep`
 * path instead, losing the wake-up and nothing else. Pinned by the suite.
 *
 * The event type the wait listens for is `DONE_EVENT_TYPE` (src/run-done.ts):
 * the done door that sends it and this wait that receives it share the one
 * constant, so the two halves cannot drift apart.
 */
export const MIN_EVENT_WAIT_MS = 1_000;

// ------------------------------------------------------------ the config ---

export type RunConfig = {
  max_wall_clock_ms: number;
  max_cost_usd: number;
  /** Whether the deployment explicitly supplied a cost budget override. */
  cost_budget_configured: boolean;
  stop_grace_ms: number;
  /** A fixed cadence when the deployment asks for one; else the backoff above. */
  poll_interval_ms: number | null;
  /** Looks per boot before the run is stopped cleanly rather than watched on. */
  max_observations: number;
  harness: string | null;
  model: string | null;
};

/**
 * Reads a positive numeric var, ignoring an unusable value with a log rather
 * than failing the run — a typo'd budget must not take the factory down, and
 * the default it falls back to is the safe direction.
 */
function positiveVar(env: Env, name: keyof Env, fallback: number, integer: boolean): number {
  const raw = env[name];
  if (typeof raw !== "string" || raw.trim() === "") return fallback;
  const parsed = Number(raw);
  const usable = integer ? Number.isSafeInteger(parsed) : Number.isFinite(parsed);
  if (!usable || parsed <= 0) {
    console.error(
      `factory run-workflow: ${String(name)} must be a positive number; ignoring "${raw}" and using ${fallback}`,
    );
    return fallback;
  }
  return parsed;
}

function textVar(env: Env, name: keyof Env): string | null {
  const raw = env[name];
  return typeof raw === "string" && raw.trim() !== "" ? raw.trim() : null;
}

function hasPositiveVar(env: Env, name: keyof Env, integer: boolean): boolean {
  const raw = textVar(env, name);
  if (raw === null) return false;
  const parsed = Number(raw);
  return (integer ? Number.isSafeInteger(parsed) : Number.isFinite(parsed)) && parsed > 0;
}

/**
 * What one submission asked its own budget to be (tick wn5).
 *
 * Both fields are optional and both are bounded by the deployment's ceiling on
 * the way in: a flag may lower a budget, never raise it. The ceiling is the
 * operator's standing decision, and a submission is something an agent can
 * make — so "cheap and bounded" is a per-invocation choice while "how much may
 * anything spend at all" stays a deployment one.
 */
export type RunBudgetOverride = {
  max_cost_usd?: number;
  max_wall_clock_ms?: number;
};

/**
 * The requested budget, clamped to the ceiling.
 *
 * An unusable value is ignored with a log rather than failing the run, exactly
 * as `positiveVar` treats a typo'd var — and both fallbacks are toward the
 * ceiling, which is the bound that was already agreed to. A submission's value
 * is validated at the edge (`parseSubmission`), so reaching this with garbage
 * means something upstream is broken, not that a run should widen its budget.
 */
function boundedBudget(
  ceiling: number,
  requested: number | undefined,
  name: string,
  integer: boolean,
): { value: number; applied: boolean } {
  if (requested === undefined) return { value: ceiling, applied: false };
  const usable = integer ? Number.isSafeInteger(requested) : Number.isFinite(requested);
  if (!usable || requested <= 0) {
    console.error(
      `factory run-workflow: ${name} must be a positive number; ignoring ${String(requested)} and using ${ceiling}`,
    );
    return { value: ceiling, applied: false };
  }
  if (requested > ceiling) {
    // Said, not silently honoured and not refused: the run is bounded either
    // way, and an operator who asked for more must be able to read back that
    // the deployment ceiling is what it actually got.
    console.error(
      `factory run-workflow: ${name} ${requested} exceeds the deployment ceiling ${ceiling}; ` +
        "a submission may lower a budget, never raise it",
    );
    return { value: ceiling, applied: true };
  }
  return { value: requested, applied: true };
}

export function runConfig(env: Env, override: RunBudgetOverride = {}): RunConfig {
  const poll = textVar(env, "RUN_POLL_INTERVAL_MS");
  const wallCeiling = positiveVar(env, "RUN_MAX_WALL_CLOCK_MS", DEFAULT_MAX_WALL_CLOCK_MS, true);
  const costCeiling = positiveVar(env, "RUN_MAX_COST_USD", DEFAULT_MAX_COST_USD, false);
  const cost = boundedBudget(costCeiling, override.max_cost_usd, "max_cost_usd", false);
  const wall = boundedBudget(wallCeiling, override.max_wall_clock_ms, "max_wall_clock_ms", true);
  return {
    max_wall_clock_ms: wall.value,
    max_cost_usd: cost.value,
    // A submission that named a cost budget has asked for one, so it is
    // enforced like a configured var — including the part that matters: a
    // budget whose telemetry cannot be read stops the run rather than letting
    // it spend unmeasured.
    cost_budget_configured: hasPositiveVar(env, "RUN_MAX_COST_USD", false) || cost.applied,
    stop_grace_ms: positiveVar(env, "RUN_STOP_GRACE_MS", DEFAULT_STOP_GRACE_MS, true),
    poll_interval_ms:
      poll === null ? null : positiveVar(env, "RUN_POLL_INTERVAL_MS", MIN_POLL_MS, true),
    max_observations: positiveVar(env, "RUN_MAX_OBSERVATIONS", MAX_OBSERVATIONS, true),
    harness: textVar(env, "RUN_HARNESS"),
    model: textVar(env, "RUN_MODEL"),
  };
}

/**
 * The budget a submission will ACTUALLY run under (tick 7zk).
 *
 * An operator asked for `--max-cost 40` and got $8, because `RUN_MAX_COST_USD`
 * was 8 and a submission may only lower a budget. That is the correct policy
 * and it was applied silently: `tk cloud run` printed nothing about $8, and the
 * first place the real number appeared was the cancellation that ended the run.
 * It is the third time in this epic a deployment ceiling replaced an operator's
 * number with no line anywhere saying so (tick 5fg found two for wall clock),
 * and `.tick/learnings.md` now carries the rule: when a bound does not take
 * effect, enumerate every layer that can lower it.
 *
 * So the submission path answers with the number that will govern, and says
 * when it is not the number that was asked for. Nothing here decides anything —
 * `runConfig` is still the one clamp — this only reports its result at the one
 * moment an operator is still reading.
 */
export type EffectiveRunBudget = {
  /** What this run may spend, in USD, after clamping. */
  max_cost_usd: number;
  /** What this run may take, in ms, after clamping. */
  max_wall_clock_ms: number;
  /** What the submission asked for, when it asked at all. */
  requested_max_cost_usd: number | null;
  requested_max_wall_clock_ms: number | null;
  /** Whether the deployment ceiling lowered what was asked for. */
  cost_clamped: boolean;
  wall_clock_clamped: boolean;
};

export function effectiveRunBudget(env: Env, override: RunBudgetOverride = {}): EffectiveRunBudget {
  const wallCeiling = positiveVar(env, "RUN_MAX_WALL_CLOCK_MS", DEFAULT_MAX_WALL_CLOCK_MS, true);
  const costCeiling = positiveVar(env, "RUN_MAX_COST_USD", DEFAULT_MAX_COST_USD, false);
  const config = runConfig(env, override);
  const asked = (value: number | undefined): number | null =>
    value === undefined || !Number.isFinite(value) || value <= 0 ? null : value;
  const cost = asked(override.max_cost_usd);
  const wall = asked(override.max_wall_clock_ms);
  return {
    max_cost_usd: config.max_cost_usd,
    max_wall_clock_ms: config.max_wall_clock_ms,
    requested_max_cost_usd: cost,
    requested_max_wall_clock_ms: wall,
    cost_clamped: cost !== null && cost > costCeiling,
    wall_clock_clamped: wall !== null && wall > wallCeiling,
  };
}

/**
 * What one observation learned about spend, carried to the next sleep.
 *
 * The rate is measured across the most recent gap rather than averaged over
 * the run: a run that idles through boot and then burns hard has an average
 * that badly understates what the next five minutes will cost.
 */
export type SpendSample = {
  cost_usd: number;
  at_ms: number;
  /** Dollars per millisecond across the most recent gap; null until one shows spend. */
  rate_usd_per_ms: number | null;
};

/**
 * Folds an observation's spend reading into the running sample.
 *
 * An observation that read no cost at all (budgets not enforced on this pass,
 * or a look that tripped before the read) leaves the previous sample standing:
 * a missing reading is not a reading of zero.
 */
export function spendSample(
  previous: SpendSample | null,
  cost_usd: number | null,
  at_ms: number,
): SpendSample | null {
  if (cost_usd === null) return previous;
  const carried = previous === null ? null : previous.rate_usd_per_ms;
  if (previous === null || at_ms <= previous.at_ms) {
    return { cost_usd, at_ms, rate_usd_per_ms: carried };
  }
  const spent = cost_usd - previous.cost_usd;
  // A quiet gap is not evidence the run stopped spending — gateway logs land in
  // batches, so a look can read the same total twice — which is why the last
  // observed rate stands rather than the window reopening to the full backoff.
  const measured = spent > 0 ? spent / (at_ms - previous.at_ms) : null;
  return { cost_usd, at_ms, rate_usd_per_ms: measured ?? carried };
}

/**
 * What the next sleep must respect beyond the backoff.
 *
 * The wall clock is the one deadline in force since tick dl8 removed the
 * closeout pass (the only pass that ever carried a window of its own), so a
 * sleep stops at it: the look that trips should be the next one. Spend is
 * only ever projected, so it gets the headroom rule below instead.
 */
export type Cadence = {
  /** Now, on the same clock as `deadline_ms` — the last checkpointed reading. */
  now_ms: number;
  /** The run's wall-clock deadline, or null before the run starts its clock. */
  deadline_ms: number | null;
  /** The last observation's spend reading, or null before the first one. */
  spend: SpendSample | null;
};

/** The backoff the cadence starts from, before any budget caps it. */
function baseDelay(config: RunConfig, n: number): number {
  if (config.poll_interval_ms !== null) return config.poll_interval_ms;
  return Math.min(Math.round(MIN_POLL_MS * POLL_BACKOFF ** n), MAX_POLL_MS);
}

/** Never sleep past a deadline: the look that trips should be the next one. */
function deadlineCap(cadence: Cadence): number | null {
  if (cadence.deadline_ms === null) return null;
  return cadence.deadline_ms - cadence.now_ms;
}

/**
 * Never sleep through more than `BUDGET_POLL_HEADROOM` of what is left.
 *
 * With no rate yet there is nothing to project from and the backoff stands —
 * a run that has not been seen spending is not the run this bounds.
 */
function spendCap(config: RunConfig, spend: SpendSample | null): number | null {
  if (spend === null) return null;
  const remaining = config.max_cost_usd - spend.cost_usd;
  // Already at or over the ceiling: the next look is the one that trips, and
  // every millisecond until it is unwatched spend.
  if (remaining <= 0) return MIN_POLL_MS;
  const rate = spend.rate_usd_per_ms;
  if (rate === null || !(rate > 0)) return null;
  return (remaining / rate) * BUDGET_POLL_HEADROOM;
}

/**
 * The gap before observation `n` (0-based) of a boot.
 *
 * Without a cadence this is the bare backoff — the pre-sleep lease renewal
 * asks for exactly that. With one, the backoff is the ceiling and the budgets
 * only ever shorten it, never below the fast cadence (or below a deliberately
 * tiny fixed interval, which is already faster than that floor).
 */
export function pollDelay(config: RunConfig, n: number, cadence: Cadence | null = null): number {
  const base = baseDelay(config, n);
  if (cadence === null) return base;

  const caps: number[] = [];
  const deadline = deadlineCap(cadence);
  if (deadline !== null) caps.push(deadline);
  const spend = spendCap(config, cadence.spend);
  if (spend !== null) caps.push(spend);
  if (caps.length === 0) return base;

  const floor = Math.min(base, MIN_POLL_MS);
  return Math.max(floor, Math.min(base, Math.round(Math.min(...caps))));
}

/**
 * The lease ttl a renewal asks for.
 *
 * It has to outlive the gap to the *next* observation, or the run would expire
 * its own lease between two looks and hand the project to a queued submission
 * while it is still working.
 */
export function renewalTtl(pollMs: number): number {
  return Math.min(Math.max(pollMs * 3, DEFAULT_LEASE_TTL_MS), MAX_LEASE_TTL_MS);
}

/**
 * What one renewal returned, kept as the RunRoom answered it (tick 7n7).
 *
 * `null` is "the renewal could not be made" — a DO hop that threw — and is
 * NOT a lost lease: a failed read has never been a stop in this file
 * (`hardStopRecord`), and treating one as a stop would kill
 * runs on a transient. `ok: false` is a verdict, and it carries WHICH of the
 * two ways the lease went.
 *
 * `reclaimed` is tick oen: the renewal found the lease lapsed with nobody
 * else holding it, and the run took it back rather than stopping. It is kept
 * apart from a plain renewal on purpose — a reclaim means the project WAS
 * unheld for a while, which is worth an operator's attention even though it
 * is no longer worth a run — and `detail` is the room's own account of the
 * state it found.
 */
export type LeaseRenewal =
  | { ok: true; reclaimed?: undefined }
  | { ok: true; reclaimed: true; detail: string }
  | { ok: false; lost: LeaseLostReason; holder: string | null; detail: string };

/**
 * Extends the run's hold on its project, and reports the verdict verbatim.
 *
 * Every renewal in this file goes through here so every caller answers the
 * same question the same way (`.tick/learnings.md`: if two endpoints answer
 * the same question, they must run the same check).
 *
 * ## A lapse is reclaimed, a take is a stop (tick oen)
 *
 * A renewal that answers `expired` is followed by a reclaim: the lease lapsed
 * and no other run holds it, so the project is simply free and this run is
 * the one that was working on it. Until tick oen that answer was a HARD stop.
 * The acquire ttl covers a ~1-minute boot and the heartbeats cover the time
 * after it, but nothing covers a boot, stall or step longer than the lease —
 * `BOOT_LEASE_TTL_MS`'s own comment says "a slow boot outlives any fixed
 * acquire ttl" — and when one happened the run stopped itself with nobody
 * else in sight. Measured on CI (2-vCPU runner, a long-pass workflow test,
 * 200ms lease): "could not renew its lease after boot 1: ... has expired
 * or been released — no dispatch lease is held for this project, and no other
 * run has taken it", then a hard stop before the container ever worked.
 *
 * The reclaim is the room's compare-and-swap, not a second decision made
 * here: between the renewal and the reclaim another run may have taken the
 * lease, and then the reclaim refuses `taken` and this run stops exactly as
 * it would have on a take — D4 is one arbiter per project, and a run that is
 * not the arbiter must not keep writing.
 */
export async function renewRunLease(
  env: Env,
  params: RunWorkflowParams,
  ttlMs: number,
): Promise<LeaseRenewal | null> {
  try {
    const room = roomFor(env, params.project);
    const renewed = await room.renewDispatchLease({
      run_id: params.run_id,
      token: params.lease_token,
      ttl_ms: ttlMs,
    });
    if (renewed.ok) return { ok: true };
    if (renewed.error !== "lease_lost") {
      // A malformed call is this supervisor's own bug, not a lost lease.
      console.error(
        `factory run-workflow: ${params.run_id} could not renew its lease: ${renewed.detail}`,
      );
      return null;
    }
    if (renewed.lost === "taken") {
      return {
        ok: false,
        lost: "taken",
        holder: renewed.holder?.run_id ?? null,
        detail: renewed.detail,
      };
    }

    const reclaimed = await room.reclaimDispatchLease({
      run_id: params.run_id,
      token: params.lease_token,
      epic: params.epic,
      origin: "cloud",
      requested_by: params.requested_by,
      ttl_ms: ttlMs,
    });
    if (reclaimed.ok) {
      // A lease that was live after all raced a renewal; nothing lapsed.
      if (!reclaimed.reclaimed) return { ok: true };
      // Never silent: the project went unheld, and the line says in what
      // state the room found it, in the room's own words.
      console.warn(
        `factory run-workflow: ${params.run_id} reclaimed its lapsed lease: ${reclaimed.detail}`,
      );
      return { ok: true, reclaimed: true, detail: reclaimed.detail };
    }
    if (reclaimed.error !== "lease_lost") {
      console.error(
        `factory run-workflow: ${params.run_id} could not reclaim its lapsed lease: ${reclaimed.detail}`,
      );
      return null;
    }
    return {
      ok: false,
      lost: "taken",
      holder: reclaimed.holder.run_id,
      detail: `${renewed.detail}; then, before it could be reclaimed, ${reclaimed.detail}`,
    };
  } catch (error) {
    console.error(
      `factory run-workflow: ${params.run_id} could not renew its lease: ${String(error)}`,
    );
    return null;
  }
}

/**
 * The stop a lost lease produces, told apart by HOW it was lost (tick 7n7).
 *
 * Both end the run — D4 is one arbiter per project, and a run that is not the
 * arbiter must not keep writing — but they are opposite failures and an
 * operator has to be able to tell which one happened. run_659b7cf2 read "the
 * dispatch lease was lost to another run" when no other run existed: its
 * ten-minute lease had simply lapsed under a long stretch of work that
 * renewed nothing. That message sent the diagnosis looking for a
 * competing run for as long as it stood.
 *
 * Since tick oen `renewRunLease` reclaims a lapsed, unheld lease instead of
 * reporting it, so the `expired` branch is no longer reached from a renewal.
 * It stays, worded as before, because the type still admits it and a stop
 * that ever does reach it must not say "taken".
 */
export function leaseLostTrip(renewal: { lost: LeaseLostReason; holder: string | null }): Trip {
  if (renewal.lost === "taken") {
    return {
      kind: "stop",
      hard: true,
      detail:
        "the dispatch lease was taken by another run" +
        (renewal.holder === null ? "" : ` (${renewal.holder})`),
    };
  }
  return {
    kind: "stop",
    hard: true,
    detail:
      "the dispatch lease expired before it was renewed — no other run has taken it, " +
      "so this run simply stopped being the project's arbiter",
  };
}

// ----------------------------------------------------------- the context ---

export type RunContext = {
  /**
   * The remote this run's containers clone.
   *
   * github.com for a `write` run, exactly as before tick pzf; this factory's
   * own read-only git door for a `read_only` one. Which of the two, and which
   * credential goes with it, is {@link git}'s answer — resolved once, here,
   * from the grade on the run record.
   */
  repo_url: string;
  /**
   * How this run reaches its repository and where its GitHub credential comes
   * from (D11, tick pzf).
   *
   * Resolved before any container exists, for the same reason `sandbox_image`
   * is: a grade this deployment cannot serve is a configuration verdict, and
   * the only alternative to refusing the run would be booting it with a
   * credential stronger than it asked for.
   */
  git: SandboxGitPlan;
  /**
   * The gateway endpoint the sandbox is pointed at: this factory's own
   * `/api/gateway` prefix, which exchanges the run's token for the operator's
   * provider key and stamps the run/tick metadata on the way to their AI
   * Gateway (D17, src/gateway.ts).
   */
  gateway_base_url: string;
  started_at_ms: number;
  config: RunConfig;
  /**
   * Why gateway cost telemetry is unavailable for a run allowed to continue
   * without an explicit cost budget. The fact is recorded rather than left to
   * look like a run that spent nothing.
   */
  cost_telemetry: string | null;
  /**
   * The remote's branch heads as they were when the run started (tick ehy).
   *
   * Finalize compares this against a second read to decide whether the epic
   * actually moved. It is taken here, before a container exists, so the
   * baseline cannot include anything this run pushed — and a read that failed
   * is carried as a failure, so an unreadable remote produces `unknown`
   * rather than a comparison against nothing.
   */
  refs_baseline: RefSnapshot;
  /**
   * The image this run's containers boot: the `[sandbox].image` its repository
   * declares at the submitted SHA, else this deployment's own (tick x3v).
   *
   * Resolved once, before any container or credential exists, because a
   * declared image this deployment cannot serve is a configuration verdict —
   * a reboot reaches the identical answer, and the run should never have been
   * started.
   */
  sandbox_image: string;
  /**
   * The pull request this run was dispatched to review (UC5, tick v7g), or
   * null for every other run.
   *
   * Read from the `pr_reviews` row keyed by this run id — the same row the
   * review door reads, so the container's boot and its one permitted write are
   * scoped by one fact rather than by two that could drift. Resolved here,
   * before any container exists, for `sandbox_image`'s reason: a review run
   * carrying a credential that could push is a configuration verdict, and the
   * only alternative to refusing it would be booting it anyway.
   */
  review: ReviewTarget | null;
};

export type ContextResult = { ok: true; context: RunContext } | { ok: false; detail: string };

/**
 * Everything the run needs before it boots anything, checked once.
 *
 * A deployment that cannot boot a container or cannot reach a gateway is a
 * broken deploy, not a run that should be attempted: both refusals happen here,
 * before a sandbox exists and before any credential is handed to one.
 */
export async function acquireContext(env: Env, params: RunWorkflowParams): Promise<ContextResult> {
  const run = await getRun(env.DB, params.run_id);
  if (run === null) {
    return { ok: false, detail: `run ${params.run_id} has no index row — it was never recorded` };
  }

  if (sandboxBinding(env) === null) {
    return {
      ok: false,
      detail:
        "the SANDBOXES binding is not configured on this deployment, so no orchestrator " +
        "sandbox can be booted; re-run `tk factory deploy`",
    };
  }

  // D17: all cloud model traffic goes through the operator's own gateway, and
  // it gets there through this factory's proxy. Absence is an actionable stop
  // naming the command that fixes it, never a silent fall back to a vendor.
  const routing = modelRoutingComplaint(env);
  if (routing !== null) return { ok: false, detail: routing };

  const config = runConfig(env, {
    ...(params.max_cost_usd === undefined ? {} : { max_cost_usd: params.max_cost_usd }),
    ...(params.max_wall_clock_ms === undefined
      ? {}
      : { max_wall_clock_ms: params.max_wall_clock_ms }),
  });
  const telemetry = await syncRunCost(env, params.run_id);
  if (!telemetry.ok) {
    console.error(
      `factory run-workflow: ${params.run_id} has no gateway cost telemetry: ${telemetry.detail}`,
    );
    if (config.cost_budget_configured) {
      // Which failure it was decides what the operator should do: a query this
      // factory got wrong is a bug report, a 5xx is a retry, and a missing
      // credential is a setup command. One message for all three sent a live
      // run's operator to reconfigure a token that was already correct.
      return {
        ok: false,
        detail:
          "the configured cost budget cannot be enforced because AI Gateway cost telemetry " +
          `could not be read: ${telemetry.detail}; ${spendFailureRemedy(telemetry.kind)}`,
      };
    }
  }

  // Before anything boots: what the remote looked like with none of this run's
  // work on it. An unreadable remote is not a refusal — the run may still do
  // real work, and the record will say the evidence could not be read.
  const refs = await snapshotRefs(env, params.project);
  if (!refs.ok) {
    console.error(
      `factory run-workflow: ${params.run_id} could not read the branches of ` +
        `${params.project} before booting: ${refs.detail}`,
    );
  }

  // Which image this run boots, decided before a container exists (tick x3v).
  //
  // The declaration lives in the repository's tracked config at the submitted
  // SHA — never in a submission parameter, because an image is arbitrary code
  // and this container holds the run's credentials. The read is best effort:
  // its parser is a second reader of a Go-owned format, so a file it cannot
  // read leaves the base image standing and the entrypoint's own check — made
  // with the authoritative reader — refuses the boot if that was wrong.
  const declared = await readDeclaredSandboxImage(env, params.project, params.base_sha);
  if (declared.unread !== null) {
    console.error(
      `factory run-workflow: ${params.run_id} booted this deployment's image because ` +
        `${declared.unread}; the container checks its own checkout`,
    );
  }
  const image = resolveSandboxImage({
    declared: declared.image,
    deployment: deploymentImage(env),
    at: params.base_sha,
  });
  if (!image.ok) return { ok: false, detail: image.detail };

  // Whether this run is a pull request review (UC5, tick v7g), from the
  // `pr_reviews` row keyed by this run id. Nothing a container said, and
  // nothing in the params blob: the row is written by the ingestion path
  // before the run exists, and it is the same row the review door reads back.
  const review = await getReviewForRun(env.DB, params.run_id);
  if (review !== null) {
    const complaint = reviewGradeComplaint(run.credential_grade);
    if (complaint !== null) return { ok: false, detail: complaint };
  }

  // Which credential this run's containers hold (D11, tick pzf), from the
  // grade on the RUN RECORD — never from the params blob, which a resumed
  // instance could be older than, and never from anything a container said.
  // An unservable grade stops the run here, before a sandbox exists: the only
  // other move would be to boot it with a credential it did not ask for.
  const git = planSandboxGit({
    grade: credentialGrade(run.credential_grade),
    project: params.project,
    operator_token: env.GITHUB_TOKEN,
    factory_url: factoryBaseURL(env),
    direct_repo_url: repoURL(params.project),
  });
  if (!git.ok) return { ok: false, detail: git.detail };

  const context: RunContext = {
    repo_url: git.plan.repo_url,
    git: git.plan,
    gateway_base_url: runGatewayEndpoint(factoryBaseURL(env)!),
    started_at_ms: Date.now(),
    config,
    cost_telemetry: telemetry.ok ? null : telemetry.detail,
    refs_baseline: refs,
    sandbox_image: image.image,
    review,
  };

  // The run identifies itself in R2 before it does anything, so a run whose
  // Workflow is lost entirely still leaves a record of what it was.
  await writeRunRecord(env.ARTIFACTS, {
    run_id: run.run_id,
    project: run.project,
    epic: run.epic,
    base_sha: run.base_sha,
    requested_by: run.requested_by,
    // From the INDEX ROW, not from the Workflow params: the row is what
    // `tk cloud logs` and `tk cloud status` answer from, so taking it from the
    // same place keeps the two records unable to disagree about which chain
    // this run belongs to.
    ...(run.trace_id === null ? {} : { trace_id: run.trace_id }),
    ...(params.notify === undefined ? {} : { notify: params.notify }),
    started_at: run.started_at,
    state: "running",
  });

  // `starting` belongs to the submit route; the Workflow owns the run now.
  await updateRunState(env.DB, params.run_id, "running");

  // The board finds out a run exists here — after the refusals above, so it
  // never draws a run that a broken deploy was about to reject, and before any
  // container boots, so the first thing an operator sees is the run appearing.
  // `publishRunEvents` cannot throw: nothing below the record above may depend
  // on a picture of it reaching a screen (tick bne).
  await publishRunEvents(env, params.project, [
    epicStarted({
      epic: params.epic,
      run_id: params.run_id,
      ...(run.trace_id === null ? {} : { trace_id: run.trace_id }),
      status: "one orchestrator container",
    }),
  ]);

  // The same moment, on the other feed (tick k7p): the board gets its own
  // protocol shape above, and the versioned run-event feed gets the line a
  // subscriber on a laptop can parse — same identity, same vocabulary, one
  // segment the CLI follows from anywhere. Inside the checkpointed `context`
  // step, so a replayed Workflow rewrites the same key rather than appending
  // a second copy; and best effort, so a run with no artifacts bucket — or a
  // full one — notices nothing.
  await appendFeed(env, {
    project: params.project,
    run_id: params.run_id,
    seq: START_FEED_SEQ,
    events: [
      runStartedFeedEvent({
        run_id: params.run_id,
        detail: "run started: one orchestrator container",
      }),
    ],
  });

  return { ok: true, context };
}

// -------------------------------------------------------------- watching ---

/** Why a run is stopping cleanly. Both kinds take the identical path (D15). */
/**
 * Why a pass ended early, and how fast the credential has to die with it.
 *
 * `hard` is the whole of tick gyl: a clean stop revokes at the END of the
 * grace window so in-flight work can land, and a budget breach or an operator
 * kill must revoke at the START of it. A run at twice its budget spending
 * through its own stop is not an unwind, it is an unmetered four minutes.
 */
export type Trip = { hard: boolean } & (
  | { kind: "stop"; detail: string }
  | { kind: "budget"; budget: "wall_clock" | "cost"; detail: string }
);

/** The reason a revocation is recorded under, so the row says which stop killed it. */
function tripRevokeReason(trip: Trip): string {
  if (trip.kind === "budget") return `budget:${trip.budget}`;
  return trip.hard ? "stopped:hard" : "stopped";
}

type Observation = {
  process: SandboxProcessState;
  exit_code: number | null;
  /** Cursor into the orchestrator's output, carried to the next observation. */
  offset: number;
  /** Segment counter, so each flush writes a new immutable object. */
  seq: number;
  trip: Trip | null;
  at_ms: number;
  /**
   * The spend this look read back, or null when it read none — a pass that
   * does not enforce budgets, or a look that tripped before the read. It feeds
   * the next sleep's cost cap, so a missing reading must stay distinguishable
   * from a reading of zero.
   */
  cost_usd: number | null;
  /**
   * The room's account of a lapsed lease this look took back (tick oen).
   * Absent on every look that renewed normally, so the checkpointed record of
   * an ordinary look is unchanged, and present on the one that reclaimed —
   * the watch step's return value is where a later diagnosis will look.
   */
  lease_reclaimed?: string;
};

type ObserveInput = {
  params: RunWorkflowParams;
  context: RunContext;
  boot: number;
  sandbox: string;
  process_id: string;
  offset: number;
  seq: number;
  poll_ms: number;
};

/**
 * One look at the run: drain the log, renew the lease, check the stop record,
 * check the budgets, report the process.
 *
 * Order matters. Output is flushed FIRST, so an observation that then decides
 * to kill the orchestrator has already preserved what it printed.
 */
export async function observe(env: Env, input: ObserveInput): Promise<Observation> {
  const { params } = input;
  const binding = sandboxBinding(env);
  if (binding === null) {
    return {
      process: "gone",
      exit_code: null,
      offset: input.offset,
      seq: input.seq,
      trip: null,
      at_ms: Date.now(),
      cost_usd: null,
    };
  }
  const sandbox = await binding.get(input.sandbox);

  let offset = input.offset;
  let seq = input.seq;
  try {
    const output = await sandbox.readOutput(input.process_id, offset);
    if (output.text !== "") {
      const wrote = await writeHarnessSegment(
        env.ARTIFACTS,
        params.project,
        params.run_id,
        input.boot,
        seq,
        output.text,
      );
      if (wrote) seq += 1;
      offset = output.offset;
    }
  } catch (error) {
    // A sandbox that cannot be read is a sandbox that is probably dying. That
    // is the process check's verdict to make, not this one's.
    console.error(
      `factory run-workflow: ${params.run_id} could not drain its harness output: ${String(error)}`,
    );
  }

  // The lease has to outlive the gap to the next look, or the run expires its
  // own lease between two observations.
  const renewal = await renewRunLease(env, params, renewalTtl(input.poll_ms));
  const leaseLost = renewal !== null && renewal.ok === false ? renewal : null;

  const view = await sandbox.getProcess(input.process_id).catch(() => null);
  const at = Date.now();

  // Budgets are enforced on every pass (tick dl8): the one pass that ever
  // declined them — the closeout, which existed to land work the budget
  // interrupted — is gone, and the kill switch's hard-stop half of the
  // check below does not ride on a budget switch any more.
  const checked = await detectTrip(env, input, at, leaseLost);

  return {
    process: view === null ? "gone" : view.state,
    exit_code: view === null ? null : view.exit_code,
    offset,
    seq,
    trip: checked.trip,
    at_ms: at,
    cost_usd: checked.cost_usd,
    ...(renewal?.ok === true && renewal.reclaimed ? { lease_reclaimed: renewal.detail } : {}),
  };
}

/**
 * One wait between looks (ticks 7eq and cr4): the completion signal when it
 * lands, the poll cadence otherwise.
 *
 * The orchestrator POSTs the factory's done door when it finishes, the Worker
 * turns that into `instance.sendEvent()`, and this step returns the moment it
 * does — the look happens immediately rather than at the next cadence tick.
 * Events are BUFFERED by the platform, so a container that finished before
 * this wait started loses nothing. Waiting instances consume no concurrency
 * slots.
 *
 * `step.waitForEvent` THROWS on expiry and the throw is CAUGHT here, because
 * expiry is the normal cadence — not a verdict, not a failure, and never a
 * reason to conclude anything about the run. A run whose callback never lands
 * is concluded by the looks: the look that follows this wait reads the process
 * and the durable layer (the branch) exactly as it would have a cadence tick
 * later, and a gone container re-boots into resume. The signal itself decides
 * nothing either way — the event is the optimisation, the branch the truth.
 *
 * Because the catch is the cadence, it must not silently eat a wait that
 * failed for some OTHER reason — an engine that refuses the call outright is
 * complained about by name, so a degraded wait is legible in the log rather
 * than a run that appears to poll at full speed for no reason.
 *
 * A cadence below the platform's documented timeout floor takes the plain
 * sleep instead ({@link MIN_EVENT_WAIT_MS}) — a sub-second poll interval is a
 * test configuration, and it must not become a platform request-shape error.
 */
async function waitDoneSignal(
  step: WorkflowStep,
  label: string,
  attempt: number,
  look: number,
  pollMs: number,
): Promise<DoneSignal | null> {
  if (pollMs < MIN_EVENT_WAIT_MS) {
    await step.sleep(`${label}:wait:${attempt}:${look}`, pollMs);
    return null;
  }
  try {
    const event = await step.waitForEvent(`${label}:signal:${attempt}:${look}`, {
      type: DONE_EVENT_TYPE,
      timeout: pollMs,
    });
    return readDoneSignal(event.payload);
  } catch (error) {
    // The timeout is the look cadence, not a failure: fall through to the look.
    const message = String((error as { message?: unknown }).message ?? error);
    if (!/timed?\s*out|timeout/i.test(message)) {
      console.error(
        "factory run-workflow: waiting for the orchestrator's completion did not time out " +
          `cleanly (${message}); the watch continues on its cadence`,
      );
    }
    return null;
  }
}

/**
 * The trip a standing HARD stop builds, so the vocabulary has one spelling
 * per verdict: `detectTrip` reads the stop record once, routes a hard stop
 * through here, and a clean stop through its own arm below.
 *
 * Hard stops earned their own branch the hard way (tick gyl): the pass that
 * once enforced no budgets read no stop record at all, an operator killing a
 * run mid-pass was talking to nobody, and every reboot minted a fresh
 * credential over their revocation. Budgets are enforced on every pass since
 * tick dl8, but the hard/clean distinction still decides whether the
 * credential dies before the grace window or after it.
 */
function hardStopTrip(stop: { requested_by: string; requested_at: string }): Trip {
  return {
    kind: "stop",
    hard: true,
    detail: `a hard stop was requested by ${stop.requested_by} at ${stop.requested_at}`,
  };
}

/** The standing hard stop for a run, or null. A read failure is not a stop. */
async function hardStopRecord(
  env: Env,
  params: RunWorkflowParams,
): Promise<{ requested_by: string; requested_at: string } | null> {
  const stop = await roomFor(env, params.project)
    .stopRequest(params.run_id)
    .catch(() => null);
  if (stop === null || stop.mode !== "hard") return null;
  return { requested_by: stop.requested_by, requested_at: stop.requested_at };
}

/** What one budget check learned: whether to trip, and the spend it read. */
type TripCheck = { trip: Trip | null; cost_usd: number | null };

/**
 * The stop and budget check.
 *
 * The operator's stop wins over a budget: it is the more specific intent, and
 * both end in the same clean stop anyway, so the only thing that differs is
 * what the run's record and the dispatch log are told.
 *
 * It also reports the spend it read, because the cadence that decides when
 * this next runs is derived from it — see `pollDelay`.
 */
async function detectTrip(
  env: Env,
  input: ObserveInput,
  at: number,
  leaseLost: { lost: LeaseLostReason; holder: string | null } | null,
): Promise<TripCheck> {
  const { params, context } = input;

  const stop = await roomFor(env, params.project)
    .stopRequest(params.run_id)
    .catch(() => null);
  if (stop !== null) {
    // The same record answers both branches, read once: a hard stop is the
    // trip `hardStopTrip` spells, a clean stop is the softer one whose
    // credential outlives the grace window.
    if (stop.mode === "hard") {
      return { trip: hardStopTrip(stop), cost_usd: null };
    }
    return {
      trip: {
        kind: "stop",
        hard: false,
        detail: `a clean stop was requested by ${stop.requested_by} at ${stop.requested_at}`,
      },
      cost_usd: null,
    };
  }

  if (leaseLost !== null) {
    // This run is no longer the project's arbiter. Exactly one `.tick/` writer
    // per project (D4), so it stops rather than racing whoever is — and it
    // stops spending immediately. WHY it is no longer the arbiter is the
    // operator's first question, so the trip answers it (tick 7n7).
    return { trip: leaseLostTrip(leaseLost), cost_usd: null };
  }

  const elapsed = at - context.started_at_ms;
  if (elapsed >= context.config.max_wall_clock_ms) {
    return {
      trip: {
        kind: "budget",
        budget: "wall_clock",
        hard: true,
        detail:
          `the wall-clock budget is exhausted: ${Math.round(elapsed / 1000)}s of ` +
          `${Math.round(context.config.max_wall_clock_ms / 1000)}s`,
      },
      cost_usd: null,
    };
  }

  // Ground truth, not a self-report: the index row's cost is read back from AI
  // Gateway logs at every observation. An agent can misreport; an invoice
  // cannot — and a telemetry read that fails leaves the last known number
  // standing rather than inventing one.
  const spend = await syncRunCost(env, params.run_id);
  if (!spend.ok) {
    console.error(
      `factory run-workflow: ${params.run_id} could not read gateway spend: ${spend.detail}`,
    );
  }
  const run = await getRun(env.DB, params.run_id).catch(() => null);
  const cost = run?.cost_usd ?? 0;
  if (cost >= context.config.max_cost_usd) {
    return {
      trip: {
        kind: "budget",
        budget: "cost",
        hard: true,
        detail: `the cost budget is exhausted: $${cost.toFixed(2)} of $${context.config.max_cost_usd.toFixed(2)}`,
      },
      cost_usd: cost,
    };
  }

  return { trip: null, cost_usd: cost };
}

// ------------------------------------------------------------ the passes ---

type PassOutcome =
  /**
   * `detail` is optional and is the pass's own account of what it did. A
   * single orchestrator pass has nothing to add — it finished the epic, which
   * is what `completed` already says.
   */
  | { kind: "completed"; boots: number; detail?: string }
  | { kind: "failed"; detail: string; boots: number }
  | { kind: "tripped"; trip: Trip; boots: number };

type PassOptions = {
  label: string;
  /**
   * Which job this pass supervises. The epic work pass boots the ONE
   * orchestrator container (`ticfac run-epic`, phases `run`/`reconcile`);
   * the PR review job boots a container that reviews one pull request and
   * posts one comment. Since tick dl8 these are the only two, and nothing
   * boots a second container when the work pass trips — the closeout pass is
   * gone.
   */
  job: "orchestrator" | "review";
  max_boots: number;
  /** What running out of observations means for this pass. */
  on_exhausted: "stop" | "fail";
};

/** Mutable across passes so sandbox names and R2 segment folders never collide. */
type BootCounter = { next: number };

/**
 * Boot one job's container, watch it, reboot it if it dies — until it
 * finishes, trips, or runs out of allowances. The two jobs are the epic
 * orchestrator and the PR review ({@link PassOptions.job}); neither may be
 * booted beside a live one, and a trip ends the run rather than booting
 * anything after it (tick dl8).
 */
async function supervisePass(
  env: Env,
  step: WorkflowStep,
  params: RunWorkflowParams,
  context: RunContext,
  counter: BootCounter,
  options: PassOptions,
): Promise<PassOutcome> {
  let lastDetail = "the orchestrator never started";
  let lastSeen: { state: SandboxProcessState; exit_code: number | null } = {
    state: "gone",
    exit_code: null,
  };

  for (let attempt = 1; attempt <= options.max_boots; attempt++) {
    // Before anything is credentialled — before a boot is even counted: does a
    // hard stop stand?
    //
    // This is the half of the kill switch that was missing. Revoking a run's
    // token stopped nothing on a live run because the very next boot minted a
    // replacement — the supervisor undoing the operator's revocation every
    // time the harness died of it, closeout boots included, until the
    // container application itself was deleted. A hard stop is therefore a
    // durable refusal to mint, not a one-off revocation (tick gyl).
    const killed = await step.do(`${options.label}:killcheck:${attempt}`, OBSERVE_RETRIES, () =>
      hardStopRecord(env, params),
    );
    if (killed !== null) {
      await step.do(`${options.label}:killrevoke:${attempt}`, OBSERVE_RETRIES, async () => {
        const revoked = await revokeRunTokens(env, params.run_id, "stopped:hard");
        return { revoked };
      });
      return {
        kind: "tripped",
        trip: {
          kind: "stop",
          hard: true,
          detail:
            `a hard stop requested by ${killed.requested_by} at ${killed.requested_at} ` +
            "stands, so no orchestrator was credentialled",
        },
        boots: counter.next - 1,
      };
    }

    const boot = counter.next++;
    // A reboot is a *fresh* container by construction: the previous one is
    // presumed broken, and reusing its name is how you inherit what broke it.
    const name = sandboxName(params.run_id, boot);
    // Only the very first boot of the epic work pass is a plain `run`; every
    // later one reconciles first, because a replacement's whole job is to
    // adopt what the dead container pushed and continue from it.
    //
    // `review` is the exception, and it is a JOB rather than a phase of the
    // orchestrator (tick dl8): a review container reads one pull request and
    // posts one comment — its own prompt, its own credential grade — and a
    // reboot of it repeats the same review, not a reconcile of an epic that
    // does not exist.
    const phase: OrchestratorPhase =
      options.job === "review" ? "review" : boot === 1 ? "run" : "reconcile";

    const booted = await step.do(
      `${options.label}:boot:${attempt}`,
      // A deliberate policy on the one step that starts paid work (cr4): a
      // config-less step inherits ten minutes of timeout AND five retries —
      // the retry default re-ran the whole pass and re-dispatched model work
      // up to five times, which is very likely what exhausted the GitHub
      // hourly budget (uim). `BOOT_RETRIES` is the deliberate retry policy;
      // `BOOT_STEP_TIMEOUT_MS` is the deliberate timeout, sized for what a
      // boot is (a cold pull and a spawn) rather than the platform's default.
      { ...BOOT_RETRIES, timeout: BOOT_STEP_TIMEOUT_MS },
      async () => {
        const binding = sandboxBinding(env);
        if (binding === null) throw new Error("the SANDBOXES binding disappeared mid-run");
        // Every boot rotates the run's gateway credential (D17). The container
        // being replaced may still be alive somewhere; its token dies before the
        // replacement's is live, so two orchestrators can never both spend
        // against one run — and the token this one gets carries the run and tick
        // ids that stamp every model request it makes.
        const credential = await issueRunToken(env, {
          run_id: params.run_id,
          tick_id: params.epic,
          attempt: boot,
        });
        // The image is a parameter of the boot, not a constant of the call site
        // (tick 3q2's seam), and since tick x3v the value can be the
        // repository's own: `acquireContext` resolved it from the tracked config
        // at the submitted SHA, and refused the run outright if this deployment
        // could not serve it. The container is told which image it got, so its
        // own reader can refuse a boot that is not what the repository declared.
        const image = context.sandbox_image;
        // keepAlive (tick cr4): this container heartbeats every 30 seconds and
        // so cannot be killed by idleness while the orchestrator works — the
        // platform's own doc is the trade: a container under keepAlive "must be
        // explicitly destroyed... to prevent containers running indefinitely
        // and counting toward your account limits". Every ending of a boot
        // destroys it in the finally below, and `finalize` sweeps as backstop.
        const sandbox = await binding.get(name, { image, keepAlive: true });
        const started = await sandbox.startProcess(ORCHESTRATOR_COMMAND, {
          env: orchestratorEnv({
            run_id: params.run_id,
            epic: params.epic,
            base_sha: params.base_sha,
            repo_url: context.repo_url,
            gateway_base_url: context.gateway_base_url,
            gateway_token: credential.token,
            phase,
            // The chain this container belongs to (tick hyi). Every boot of the
            // orchestrator carries it, including a reconcile's replacement: the
            // replacement is the same causal chain as the sandbox it succeeds.
            ...(params.trace_id === undefined ? {} : { trace_id: params.trace_id }),
            // The grade's teeth (tick pzf): `operator` hands over the token
            // that can push, `run` hands over this run's own `tkr_` credential,
            // which github.com will not accept and this factory's git door will
            // not forward a push for.
            github_token: containerGitToken(context.git, env.GITHUB_TOKEN, credential.token),
            // Which harness and model the container's entrypoint probes before
            // it starts its job. The two jobs are routed differently (tick dl8):
            //
            // - the ORCHESTRATOR container execs `ticfac run-epic`, so its
            //   harness/model pair only has to satisfy the entrypoint's
            //   pre-flight probes — the deployment's run-level choice
            //   (RUN_HARNESS/RUN_MODEL) stands, as wrangler.toml pins it;
            // - the REVIEW job is routed like every other cloud role, through
            //   the worker ladder (`workerHarness`/`workerModel`), whose floor
            //   is pi on GLM — never the image's own harness selection, which
            //   a deployment that routes nothing would leave at claude (the
            //   xte finding dl8 absorbed).
            ...(options.job === "review"
              ? {
                  harness: workerHarness(context.config.harness, env.RUN_WORKER_HARNESS),
                  model: workerModel(context.config.model, env.RUN_WORKER_MODEL),
                }
              : {
                  ...(context.config.harness === null ? {} : { harness: context.config.harness }),
                  ...(context.config.model === null ? {} : { model: context.config.model }),
                }),
            sandbox_image: image,
            // The factory URL is given per BOOT (tick 7eq): every orchestrator
            // reports its own finish to the done door over it, and the same
            // URL is what its `ticfac run-epic` hands the per-tick sandbox
            // door's client — the one dispatch path a container has.
            ...(factoryBaseURL(env) === null
              ? {}
              : { factory_url: factoryBaseURL(env)!, factory_project: params.project }),
            // The review half (tick v7g). Given per BOOT, from the run's own row
            // — a container is told which pull request it is reading, and there
            // is no other way for it to find out. The factory URL comes with it
            // because that is where the findings go; a review container that
            // could not reach the door would have nowhere to put its one output.
            ...(context.review === null || factoryBaseURL(env) === null
              ? {}
              : {
                  review_pr: context.review.pr_number,
                  review_head_sha: context.review.head_sha,
                  factory_url: factoryBaseURL(env)!,
                  factory_project: params.project,
                }),
          }),
        });
        return { process_id: started.id, at_ms: Date.now() };
      },
    );

    // Renew the lease the moment the container is up, BEFORE the first sleep.
    //
    // The lease is acquired at submit, and until this existed the next renewal
    // was the first observation — which sits behind boot plus a poll delay.
    // A boot that clones, installs a toolchain, probes the model and the
    // harness and runs pre-flight takes about a minute, so the first renewal
    // reliably arrived after the lease it was renewing had already expired.
    // renewDispatchLease then answered lease_lost, detectTrip read that as a
    // HARD trip, and the run revoked its own gateway token roughly a minute
    // in: measured on run_d941c5ee as a 403 run_token_revoked on the harness's
    // first real call, with nobody having asked for a stop (tick 4ef).
    //
    // Acquiring for longer is the other half of the fix (BOOT_LEASE_TTL_MS in
    // runs.ts) and neither half is sufficient alone: a slow boot outlives any
    // fixed acquire ttl, and a renewal that only happens after the first sleep
    // is always too late. A failure here is not fatal on its own — the first
    // observation re-reads the lease and trips properly if it really is gone.
    //
    // What it returns is the VERDICT, not a boolean. `{"ok":false}` is what
    // this step recorded on run_659b7cf2, and it is the reason the diagnosis
    // of that run had to start from a guess: the one call that knew whether
    // the lease had been taken or had merely lapsed threw the answer away
    // (`.tick/learnings.md`: persist a remote step's return value at its
    // return site, before anything interprets it).
    //
    // A boot that outlived the lease is the case tick oen is about: the lease
    // lapsed with nobody else holding it, `renewRunLease` took it back, and
    // this step records `reclaimed` with the room's account — never a bare
    // `{"ok":true}` that would erase the lapse, and never the stop it used to
    // be.
    await step.do(`${options.label}:lease:${attempt}`, OBSERVE_RETRIES, async () => {
      const renewal = await renewRunLease(env, params, renewalTtl(pollDelay(context.config, 0)));
      if (renewal === null) return { ok: false, unreadable: true };
      if (renewal.ok && renewal.reclaimed) {
        return { ok: true, reclaimed: true, detail: renewal.detail };
      }
      if (renewal.ok) return { ok: true };
      console.error(
        `factory run-workflow: ${params.run_id} could not renew its lease after boot ${boot}: ` +
          `${renewal.detail}`,
      );
      return { ok: false, lost: renewal.lost, holder: renewal.holder, detail: renewal.detail };
    });

    // The container exists and is credentialed, so from here on every ending
    // of this boot — completed, tripped, out of looks, dead, thrown — destroys
    // it in the finally below (tick cr4). A container booted under keepAlive
    // NEVER idles away, which is the point of the boot above and the whole of
    // the price: without this finally the run would leave it billing until an
    // operator noticed, and `finalize`'s sweep would arrive far too late to be
    // the only destroy.
    try {
      // The one absolute deadline a sleep on this pass must not run past: the
      // run's wall clock, enforced on every pass since tick dl8 removed the
      // closeout pass (the only pass that ever carried a window of its own).
      const cadenceDeadline = context.started_at_ms + context.config.max_wall_clock_ms;

      let offset = 0;
      let seq = 1;
      // Assume the container outlives the watch until an observation says
      // otherwise: falling out of the loop with this unchanged means the
      // orchestrator is still ALIVE, which is a different problem from a dead one.
      let ending: "dead" | "exhausted" = "exhausted";
      // What the last look knew about spend, and when it knew it. Both come from
      // checkpointed step results, never a live `Date.now()`, so a replayed
      // Workflow recomputes the identical cadence.
      let spend: SpendSample | null = null;
      let lastAt = booted.at_ms;

      for (let look = 0; look < context.config.max_observations; look++) {
        const pollMs = pollDelay(context.config, look, {
          now_ms: lastAt,
          deadline_ms: cadenceDeadline,
          spend,
        });
        // The wait for THIS look (ticks cr4 and 7eq): `step.waitForEvent` on
        // the orchestrator's completion signal, with the poll cadence as its
        // timeout — whichever lands first. The container's own "I am done"
        // (the done door) wakes the supervisor the moment it lands instead of
        // at the next cadence slice, while a run with nothing to say is still
        // looked at on the cadence the budgets below are enforced on. The
        // timeout THROWING is not a verdict — it is the cadence — and the
        // event only collapses the wait: the look below still reads the
        // process and the budgets, and the run's verdict still comes from the
        // durable layer, never from the container's claim.
        const signal = await waitDoneSignal(step, options.label, attempt, look, pollMs);
        if (signal !== null) {
          // Recorded, never trusted: the one durable trace that the callback
          // landed and was consumed, for whoever asks later why a run settled
          // without a reboot. The payload itself rides the checkpointed
          // `signal` step's own output, which `GET /api/runs/:id` serves.
          await step.do(`${options.label}:heard:${attempt}:${look}`, OBSERVE_RETRIES, async () => {
            await logDispatch(env, {
              run_id: params.run_id,
              epic: params.epic,
              decision: "signal:done",
              reason: null,
            });
            return { heard: signal };
          });
        }

        const seen = await step.do(
          `${options.label}:watch:${attempt}:${look}`,
          OBSERVE_RETRIES,
          async () =>
            observe(env, {
              params,
              context,
              boot,
              sandbox: name,
              process_id: booted.process_id,
              offset,
              seq,
              poll_ms: pollMs,
            }),
        );
        offset = seen.offset;
        seq = seen.seq;
        spend = spendSample(spend, seen.cost_usd, seen.at_ms);
        lastAt = seen.at_ms;

        if (seen.trip !== null) {
          const trip = seen.trip;
          const reason = tripRevokeReason(trip);
          const revoke = (label: string) =>
            step.do(`${options.label}:${label}:${attempt}`, OBSERVE_RETRIES, async () => {
              const revoked = await revokeRunTokens(env, params.run_id, reason);
              return { revoked };
            });

          // The kill switch, at the layer that does not need the agent's
          // cooperation (D17): whatever survived the kill cannot spend another
          // cent, because its gateway token is dead.
          //
          // WHEN it fires is the difference a live run paid for. A clean stop
          // revokes after the grace window, because the point of that window is
          // to let in-flight work land. A budget breach or an operator kill has
          // no such claim on the money: the run is already over its allowance,
          // so the credential dies FIRST and the unwind happens on a container
          // that can no longer spend (tick gyl).
          if (trip.hard) await revoke("revoke");
          // The in-flight work still gets its bounded window to land, then the
          // container is killed. Nothing durable is lost either way — the
          // keeper pushes as the run works, and `ticfac run-epic`'s own SIGTERM
          // path commits and pushes on the way out — so the branch is the
          // run's state, and no replacement is booted (tick dl8).
          await step.sleep(`${options.label}:grace:${attempt}`, context.config.stop_grace_ms);
          await step.do(`${options.label}:drain:${attempt}`, OBSERVE_RETRIES, () =>
            drainAndKill(env, params, name, booted.process_id, boot, offset, seq),
          );
          // A clean stop's credential outlives the grace window (that is the
          // point of the window) and dies with the run at finalize; a hard
          // stop's already died above.
          if (!trip.hard) await revoke("revoke:clean");
          return { kind: "tripped", trip, boots: counter.next - 1 };
        }

        if (seen.process === "completed" && (seen.exit_code ?? 0) === 0) {
          return { kind: "completed", boots: counter.next - 1 };
        }

        if (seen.process === "completed" || seen.process === "failed" || seen.process === "gone") {
          const code = seen.exit_code;
          lastDetail =
            seen.process === "gone"
              ? `the orchestrator sandbox died (boot ${boot})`
              : `the orchestrator exited ${code ?? "unknown"} (boot ${boot})`;
          if (isTerminalExit(code)) {
            // A configuration verdict from the boot: the SHA still will not check
            // out, the pre-flight still fails, the epic the run was submitted
            // for is still missing from the submitted tree. Another container
            // reaches the identical answer and only costs money — so the reason
            // the run STOPS with names the class, not just the code
            // (terminalExitReason, ticfac tick rf3).
            return {
              kind: "failed",
              detail: `${lastDetail} — a configuration failure (${terminalExitReason(code ?? -1)}), so no sandbox was rebooted`,
              boots: counter.next - 1,
            };
          }
          lastSeen = { state: seen.process, exit_code: code };
          ending = "dead";
          break;
        }
      }

      if (ending === "exhausted") {
        // The orchestrator is still running and this instance is out of looks.
        // Stop it cleanly — never boot a replacement beside a live one.
        await step.do(`${options.label}:drain:${attempt}`, OBSERVE_RETRIES, () =>
          drainAndKill(env, params, name, booted.process_id, boot, offset, seq),
        );
        const detail = `the run outlived its observation budget (${context.config.max_observations} looks)`;
        return options.on_exhausted === "fail"
          ? { kind: "failed", detail, boots: counter.next - 1 }
          : {
              kind: "tripped",
              trip: { kind: "budget", budget: "wall_clock", hard: true, detail },
              boots: counter.next - 1,
            };
      }

      // The container is done for. Record why, then boot a replacement whose
      // first instruction reconciles.
      if (attempt < options.max_boots) {
        await step.do(`${options.label}:reconcile:${attempt}`, OBSERVE_RETRIES, async () => {
          await writeReconcileRecord(env.ARTIFACTS, params.project, {
            run_id: params.run_id,
            attempt: boot,
            at: new Date().toISOString(),
            previous: lastSeen,
            detail: lastDetail,
          });
          await logDispatch(env, {
            run_id: params.run_id,
            epic: params.epic,
            decision: `reboot:${boot}`,
            reason: null,
          });
          // The dying container itself is destroyed by the finally below, the
          // moment this step returns — before the replacement is booted — so the
          // orchestrator inside it cannot come back to life beside its
          // replacement: exactly one `.tick/` writer per project (D4).
          return { logged: true };
        });
      }
    } finally {
      // A container booted keepAlive must be explicitly destroyed (the SDK's
      // own rule). This runs on every path that leaves this boot's watch, so
      // the only way a keepAlive container outlives the Workflow is a Workflow
      // instance that never runs again at all — and `finalize`'s sweep still
      // destroys every boot as the backstop for exactly that case.
      await step.do(`${options.label}:destroy:${attempt}`, OBSERVE_RETRIES, async () => {
        const binding = sandboxBinding(env);
        if (binding === null) return { destroyed: false };
        try {
          const sandbox = await binding.get(name);
          await sandbox.destroy();
          return { destroyed: true };
        } catch (error) {
          console.error(
            `factory run-workflow: ${params.run_id} could not destroy sandbox ${boot}: ${String(error)}`,
          );
          return { destroyed: false };
        }
      });
    }
  }

  return {
    kind: "failed",
    detail: `${lastDetail}; ${options.max_boots} orchestrator boots were not enough`,
    boots: counter.next - 1,
  };
}

/** Last flush before the orchestrator is killed, so its final words survive. */
async function drainAndKill(
  env: Env,
  params: RunWorkflowParams,
  name: string,
  processID: string,
  boot: number,
  offset: number,
  seq: number,
): Promise<{ killed: boolean }> {
  const binding = sandboxBinding(env);
  if (binding === null) return { killed: false };
  const sandbox = await binding.get(name);
  try {
    const output = await sandbox.readOutput(processID, offset);
    await writeHarnessSegment(env.ARTIFACTS, params.project, params.run_id, boot, seq, output.text);
  } catch (error) {
    console.error(
      `factory run-workflow: ${params.run_id} could not make a final log flush: ${String(error)}`,
    );
  }
  await sandbox.killProcess(processID).catch((error: unknown) => {
    console.error(
      `factory run-workflow: ${params.run_id} could not kill its orchestrator: ${String(error)}`,
    );
  });
  return { killed: true };
}

// ------------------------------------------------------------- finalizing ---

export type RunOutcome = {
  state: "completed" | "stopped" | "failed";
  detail: string;
  boots: number;
};

/**
 * Release the lease, close the index row, finish the artifact tree.
 *
 * Every branch of the run ends here, including the ones that never booted
 * anything: a run that ends without releasing its lease wedges the project
 * until the ttl expires, which is a self-inflicted outage.
 */
export async function finalize(
  env: Env,
  params: RunWorkflowParams,
  outcome: RunOutcome,
  boots: number,
  costTelemetry: string | null = null,
  progress: RunProgress = unverifiedProgress("the run ended before its progress was assessed"),
): Promise<void> {
  const endedAt = new Date().toISOString();

  // Model access ends when the run does, whatever else happens below. A run
  // whose lease release fails is a delay; a run that leaves a live gateway
  // credential behind is a container that can still spend (D17).
  await revokeRunTokens(env, params.run_id, `finished:${outcome.state}`).catch((error: unknown) => {
    console.error(
      `factory run-workflow: ${params.run_id} could not revoke its gateway tokens: ${String(error)}`,
    );
    return 0;
  });

  // One last telemetry read, so the closing record carries what the run
  // actually spent rather than what it had spent at the last observation.
  const finalSpend = await syncRunCost(env, params.run_id);
  const telemetry = finalSpend.ok ? null : (costTelemetry ?? finalSpend.detail);

  const run = await getRun(env.DB, params.run_id);

  // The run's last word to the board (tick bne), before the index row goes
  // terminal rather than after — so "the run is finished" cannot be true
  // anywhere before the picture of it has been offered, and so nothing
  // downstream of this line (the lease release, the record, the container
  // teardown) can be reordered by how slow a board is.
  //
  // `spend` is the AI Gateway read taken above and nothing else:
  // `epicCompleted` has no parameter an agent's self-reported cost would fit,
  // which is what keeps a self-report from re-entering the protocol through
  // `metrics.costUsd`. A read that failed publishes NO cost rather than a
  // zero — unknown and free are different facts about a run.
  await publishRunEvents(env, params.project, [
    epicCompleted({
      epic: params.epic,
      run_id: params.run_id,
      state: outcome.state,
      detail: outcome.detail,
      spend: finalSpend,
      ...(params.trace_id === undefined ? {} : { trace_id: params.trace_id }),
      ...(run?.started_at === undefined
        ? {}
        : { duration_ms: Math.max(0, Date.parse(endedAt) - Date.parse(run.started_at)) }),
    }),
  ]);

  // The run's own terminal line, on the versioned feed a laptop follows (tick
  // k7p) — the same `run_finished` stage the local reconciler writes, with the
  // run's id and its outcome in the fields, never in prose alone. Best effort
  // like everything on this feed: a finalize that could not write it is still
  // a finalize, and the durable record below is where the verdict lives.
  await appendFeed(env, {
    project: params.project,
    run_id: params.run_id,
    seq: FINAL_FEED_SEQ,
    events: [
      runFinishedFeedEvent({
        run_id: params.run_id,
        detail: `${outcome.state}: ${outcome.detail}`,
      }),
    ],
  });

  // The dispatch log's own closing line, written before the row goes terminal
  // for the same reason the board event is: a caller who polls state and sees
  // it flip to a terminal value must find `finished:<state>` already in the
  // log, not a window where the row says done and the log does not yet agree.
  // Best-effort like the release below — losing one audit line must not cost
  // the run its terminal state.
  await logDispatch(env, {
    run_id: params.run_id,
    epic: params.epic,
    decision: `finished:${outcome.state}`,
    reason: null,
  }).catch((error: unknown) => {
    console.error(
      `factory run-workflow: ${params.run_id} could not log its finish decision: ${String(error)}`,
    );
  });

  await updateRunState(env.DB, params.run_id, outcome.state, endedAt);
  // The evidence the state was decided from, stamped beside it: an operator
  // reading `stopped` has to be able to see whether the run stopped having done
  // work or stopped having done nothing, without re-deriving it from a log.
  await recordRunProgress(
    env.DB,
    params.run_id,
    { progress: progress.state, detail: progress.detail },
    endedAt,
  ).catch((error: unknown) => {
    console.error(
      `factory run-workflow: ${params.run_id} could not stamp its progress verdict: ${String(error)}`,
    );
  });

  // The index row first, the Workflow params second: the row is what every
  // read surface answers from, and a run whose row was written before this
  // field existed still has whatever its instance was created with.
  const traceID = run?.trace_id ?? params.trace_id ?? "";
  const record: RunRecord = {
    run_id: params.run_id,
    project: params.project,
    epic: params.epic,
    base_sha: params.base_sha,
    requested_by: params.requested_by,
    // The finished record names the chain too, so R2 alone answers "which
    // message produced this run" for a run whose D1 row is long since read.
    ...(traceID === "" ? {} : { trace_id: traceID }),
    ...(params.notify === undefined ? {} : { notify: params.notify }),
    started_at: run?.started_at ?? endedAt,
    state: outcome.state,
    ended_at: endedAt,
    cost_usd: run?.cost_usd ?? 0,
    cost_source: telemetry === null ? "gateway" : `unavailable: ${telemetry}`,
    detail: outcome.detail,
    progress: progress.state,
    progress_detail: progress.detail,
    attempts: boots,
  };
  await writeRunRecord(env.ARTIFACTS, record);
  await writeCombinedHarnessLog(env.ARTIFACTS, params.project, params.run_id).catch(
    (error: unknown) => {
      console.error(
        `factory run-workflow: ${params.run_id} could not write a combined harness log: ${String(error)}`,
      );
    },
  );

  // Tear down every container this run booted — and only those. `destroy` on a
  // sandbox that is already gone is a no-op worth attempting (a leaked
  // container bills), but *addressing* a sandbox creates it, so a run that
  // never booted one must not reach for one on its way out.
  const binding = sandboxBinding(env);
  if (binding !== null) {
    for (let boot = 1; boot <= boots; boot++) {
      try {
        const sandbox = await binding.get(sandboxName(params.run_id, boot));
        await sandbox.destroy();
      } catch (error) {
        console.error(
          `factory run-workflow: ${params.run_id} could not destroy sandbox ${boot}: ${String(error)}`,
        );
      }
    }
  }

  try {
    await roomFor(env, params.project).releaseDispatchLease({
      run_id: params.run_id,
      token: params.lease_token,
    });
  } catch (error) {
    // The lease expires on the RunRoom's alarm anyway; losing the release is a
    // delay, not a wedge, and must not fail a finalize that already landed.
    console.error(
      `factory run-workflow: ${params.run_id} could not release its lease: ${String(error)}`,
    );
  }
}

// -------------------------------------------------------------- the run ---

/** The dispatch-log reason a trip is recorded under, from the closed vocabulary. */
function tripReason(trip: Trip): "budget_exhausted" | null {
  return trip.kind === "budget" && trip.budget === "cost" ? "budget_exhausted" : null;
}

/**
 * What the durable layer says happened, read at the end of the run.
 *
 * The second half of the comparison the context opened. Deliberately its own
 * function so it is one Workflow step: the read is a network call, and a run
 * must not lose a finalize to a GitHub hiccup.
 */
export async function assessProgress(
  env: Env,
  params: RunWorkflowParams,
  context: RunContext,
): Promise<RunProgress> {
  return compareSnapshots(context.refs_baseline, await snapshotRefs(env, params.project));
}

/**
 * The outcome the evidence supports, which is not always the one the process
 * suggested.
 *
 * This is the whole point of tick ehy. A harness that exits 0 has said it has
 * nothing more to do; it has NOT said it did anything, and only the durable
 * layer can say that. So:
 *
 * - evidence of change  → `completed`. The epic moved.
 * - no change at all    → `stopped`. Work-preserving, nothing lost, nothing
 *                         done — and visibly different from a run that
 *                         advanced the epic, which is what an operator needs
 *                         to see before submitting the same epic again.
 * - evidence unreadable → `completed`, saying so. Downgrading a run that may
 *                         well have done the work, on the strength of a
 *                         GitHub 503, would invent a failure exactly the way
 *                         inferring success invents a success.
 *
 * Only a `completed` outcome is revisited: a stop or a failure already carries
 * a truer reason than "nothing moved", and `run_progress` records the verdict
 * for those runs regardless.
 */
export function applyProgress(outcome: RunOutcome, progress: RunProgress): RunOutcome {
  if (outcome.state !== "completed") return outcome;
  switch (progress.state) {
    case "none":
      return {
        state: "stopped",
        // The outcome's own account is kept, not replaced (tick 074):
        // "nothing moved" without the run's own account of itself leaves an
        // operator with a stop and no way to tell which stop it was.
        detail:
          `${outcome.detail}, but the epic did not move: ${progress.detail}. ` +
          "An exit status is not completion",
        boots: outcome.boots,
      };
    case "unknown":
      return {
        state: "completed",
        detail: `${outcome.detail}; progress could not be verified (${progress.detail})`,
        boots: outcome.boots,
      };
    default:
      return {
        state: "completed",
        detail: `${outcome.detail}; ${progress.detail}`,
        boots: outcome.boots,
      };
  }
}

/**
 * The pull request review JOB, start to finish (UC5, tick v7g; re-homed as a
 * job the supervisor runs by tick dl8 — the one boot a harness still serves,
 * because reviewing a diff and writing prose is a job, not control flow).
 *
 * Deliberately short, and every way in which it is shorter than the epic
 * lifecycle above is a property of the job rather than a simplification:
 *
 *  - **One pass, no follow-on boot.** A review run lands nothing and writes
 *    no tracker state — it holds a read-only credential — so there is nothing
 *    to reconcile and a second paid container would do nothing but cost
 *    money. A trip ends the job exactly as the epic pass's trip ends the run.
 *  - **No ref comparison.** Tick ehy's rule stands, but the evidence changes:
 *    a run that cannot push can never move a ref, so asking whether one moved
 *    would report every review as "nothing happened". What a review run
 *    durably produces is a COMMENT, and {@link reviewEvidence} reads it from
 *    the row the review door wrote.
 *  - **The budgets are the same ones.** Cost and wall clock are enforced by
 *    the same pass machinery as any other run, because an autonomous loop with
 *    no ceiling is the one thing worse than a bad review.
 *  - **Routed like every other cloud role.** Its harness and model come from
 *    the worker ladder (`workerHarness`/`workerModel`), whose floor is pi on
 *    GLM — never the image's own harness selection, which a deployment that
 *    routes nothing would leave at claude (the xte finding dl8 absorbed).
 */
export async function superviseReview(
  env: Env,
  step: WorkflowStep,
  params: RunWorkflowParams,
  context: RunContext,
  counter: BootCounter,
): Promise<RunOutcome> {
  const work = await supervisePass(env, step, params, context, counter, {
    label: "review",
    job: "review",
    max_boots: MAX_SANDBOX_BOOTS,
    // A review that ran out of observations is over: there is nothing in
    // flight that a further pass could finish.
    on_exhausted: "fail",
  });

  const attempted: RunOutcome =
    work.kind === "completed"
      ? { state: "completed", detail: work.detail ?? "the reviewer finished", boots: work.boots }
      : work.kind === "failed"
        ? { state: "failed", detail: work.detail, boots: work.boots }
        : {
            state: "stopped",
            detail: work.trip.detail,
            boots: work.boots,
          };

  const evidence = await step.do("review:evidence", OBSERVE_RETRIES, () =>
    reviewEvidence(env.DB, params.run_id),
  );
  const progress: RunProgress = {
    state: evidence.posted ? "advanced" : "none",
    detail: evidence.detail,
  };
  // Nothing above this line may call the review done. A harness that exits 0
  // has said it has nothing more to do; only the comment says it did anything.
  const outcome: RunOutcome =
    attempted.state !== "completed"
      ? attempted
      : evidence.posted
        ? { ...attempted, detail: `${attempted.detail}; ${evidence.detail}` }
        : {
            state: "stopped",
            detail: `${attempted.detail}, but ${evidence.detail}`,
            boots: attempted.boots,
          };

  await step.do("finalize", FINALIZE_RETRIES, async () => {
    await finalize(env, params, outcome, outcome.boots, context.cost_telemetry, progress);
    return { finalized: true };
  });
  return outcome;
}

/**
 * The whole lifecycle, exported so it reads as one thing rather than as a class
 * body: context, work, clean stop if something tripped, finalize.
 */
export async function superviseRun(
  env: Env,
  params: RunWorkflowParams,
  step: WorkflowStep,
): Promise<RunOutcome> {
  const acquired = await step.do("context", CONTEXT_RETRIES, () => acquireContext(env, params));
  if (!acquired.ok) {
    const outcome: RunOutcome = { state: "failed", detail: acquired.detail, boots: 0 };
    const never = unverifiedProgress(
      "the run never booted an orchestrator, so nothing could have advanced the epic",
    );
    await step.do("finalize", FINALIZE_RETRIES, async () => {
      await finalize(env, params, outcome, 0, null, never);
      return { finalized: true };
    });

    return outcome;
  }
  const context = acquired.context;
  const counter: BootCounter = { next: 1 };

  // The PR review job (UC5, tick v7g): one container, one comment, done. It
  // is checked FIRST because it is the narrower fact: a review run implements
  // no ticks — and giving it the epic path below would boot a container to
  // close out an epic that does not exist.
  if (context.review !== null) {
    return await superviseReview(env, step, params, context, counter);
  }

  // One orchestrator container, supervised (tick l6t): the container runs
  // `ticfac run-epic` and dispatches every tick's worker itself, through the
  // cloudflare-sandbox executor and the per-tick sandbox door. The Workflow
  // boots, budgets, watches, retries and finalizes — it does not orchestrate.
  const work = await supervisePass(env, step, params, context, counter, {
    label: "work",
    job: "orchestrator",
    max_boots: MAX_SANDBOX_BOOTS,
    on_exhausted: "stop",
  });

  let outcome: RunOutcome;
  if (work.kind === "completed") {
    outcome = {
      state: "completed",
      detail: work.detail ?? "the orchestrator finished the epic",
      boots: work.boots,
    };
  } else if (work.kind === "failed") {
    outcome = { state: "failed", detail: work.detail, boots: work.boots };
  } else {
    // `tripped` is an interruption: the clean stop, identical for a budget and
    // for an operator (D15). The run ENDS here (tick dl8): the pass above has
    // already revoked, given the work its grace window, drained and killed the
    // container, and the branch — pushed as the run worked — is the state a
    // new run re-derives from. No second container is booted; a closeout boot
    // would only re-run the epic, because `ticfac run-epic` reads no phase and
    // no stop reason. What is recorded is the reason, and nothing else.
    const trip = work.trip;

    await step.do("stop:record", OBSERVE_RETRIES, async () => {
      await logDispatch(env, {
        run_id: params.run_id,
        epic: params.epic,
        decision: trip.kind === "budget" ? `stopping:budget:${trip.budget}` : "stopping:operator",
        reason: tripReason(trip),
      });
      await updateRunState(env.DB, params.run_id, "stopping");
      return { logged: true };
    });

    // A run that was stopped is `stopped`: the stop is the truer fact about it.
    outcome = { state: "stopped", detail: trip.detail, boots: work.boots };
  }

  // Nothing above this line may call the run complete. The passes report what
  // the PROCESS did; the durable layer reports what the RUN did, and only the
  // second one can promote an exit into a completion (tick ehy).
  const progress = await step.do("progress", OBSERVE_RETRIES, () =>
    assessProgress(env, params, context),
  );
  outcome = applyProgress(outcome, progress);

  await step.do("finalize", FINALIZE_RETRIES, async () => {
    await finalize(env, params, outcome, outcome.boots, context.cost_telemetry, progress);
    return { finalized: true };
  });
  return outcome;
}

/**
 * The Workflow itself. It is deliberately a thin shell: everything worth
 * testing is in `superviseRun`, and everything durable is a `step`.
 */
export class RunWorkflow extends WorkflowEntrypoint<Env, RunWorkflowParams> {
  override async run(
    event: Readonly<WorkflowEvent<RunWorkflowParams>>,
    step: WorkflowStep,
  ): Promise<RunOutcome> {
    return superviseRun(this.env, event.payload, step);
  }
}
