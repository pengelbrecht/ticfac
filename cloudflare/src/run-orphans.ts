/**
 * Finishing the run records whose supervisor is gone (tick gbg).
 *
 * A run's record goes terminal in exactly one place: its Run Workflow's
 * finalize. A Workflow that ends WITHOUT reaching finalize — an instance that
 * errored or was terminated, a stop whose supervisor had already died, an
 * instance Cloudflare no longer has at all — leaves the record at `starting`,
 * `running` or `stopping` forever. That record is counted as live by every
 * reader: status lists it, a takeover treats its claims as held, and the
 * deploy refuses to delete the 0.x container application while a "live" run
 * is on it. Evidence: run_9e40b956, run_2e66e765 and run_f15efdfb (epic 72y,
 * 2026-08) sat at `stopping` for six weeks with no Workflow instance on
 * Cloudflare, and held the ticks-orchestrator application in place after the
 * umq cutover.
 *
 * The hourly sweep here finishes such a record — but only on a CONFIRMED
 * ending. Two answers about an instance look alike from a `catch` and mean
 * opposite things: "Cloudflare has no such instance" is a fact about the run,
 * "the read failed" is a fact about the read. Finishing a live run's record on
 * a failed read would be worse than a stuck record, so the two are separated
 * once, here ({@link readRunInstance}), and only the first ever finishes
 * anything.
 */

import { getRun, type Run, recordRunProgress } from "./db";
import { revokeRunTokens } from "./gateway";
import type { Env } from "./index";
import {
  ACTIVE_RUN_STATES,
  logDispatch,
  type RunWorkflowBinding,
  roomFor,
  runWorkflowBinding,
} from "./runs";

// ------------------------------------------------------- the instance read ---

/** Workflow instance statuses after which nothing will ever advance the run. */
export const INSTANCE_ENDED: ReadonlySet<string> = new Set(["complete", "errored", "terminated"]);

/**
 * What Cloudflare said about a run's Workflow instance, with its three
 * answers kept apart:
 *  - `present`: the instance exists, with this status;
 *  - `absent`: Cloudflare says there is no such instance;
 *  - `unreadable`: the read failed for any other reason — nothing is known.
 */
export type InstanceRead =
  | { kind: "present"; id: string; status: string }
  | { kind: "absent"; detail: string }
  | { kind: "unreadable"; detail: string };

/**
 * The Workflows platform's error code for a missing instance. The binding
 * raises it as an Error whose message carries the code — production says
 * `(instance.not_found) Instance not found`, the local runtime
 * `instance.not_found`; the REST API answers the same case with code 10400 and
 * `workflows.api.error.instance.not_found`. The binding has no typed error
 * class, so this code is the one contract there is, and this is the only place
 * that reads it.
 */
export const INSTANCE_NOT_FOUND_CODE = "instance.not_found";

/** Whether an error thrown by the Workflows binding is its not-found. */
export function isInstanceNotFound(error: unknown): boolean {
  return error instanceof Error && error.message.includes(INSTANCE_NOT_FOUND_CODE);
}

/** Reads a run's Workflow instance, telling a missing instance from a failed read. */
export async function readRunInstance(
  binding: RunWorkflowBinding,
  runID: string,
): Promise<InstanceRead> {
  let instance: Awaited<ReturnType<RunWorkflowBinding["get"]>>;
  try {
    instance = await binding.get(runID);
  } catch (error) {
    if (isInstanceNotFound(error)) {
      return { kind: "absent", detail: `Cloudflare has no Workflow instance ${runID}` };
    }
    return {
      kind: "unreadable",
      detail: `the Workflow instance could not be read: ${String(error)}`,
    };
  }
  try {
    const status = await instance.status();
    return { kind: "present", id: instance.id, status: String(status.status) };
  } catch (error) {
    if (isInstanceNotFound(error)) {
      return { kind: "absent", detail: `Cloudflare has no Workflow instance ${runID}` };
    }
    return {
      kind: "unreadable",
      detail: `the Workflow instance's status could not be read: ${String(error)}`,
    };
  }
}

/** The supervisor's ending, when it has CERTAINLY ended; null otherwise. */
export function endedSupervisorOf(read: InstanceRead): { status: string; detail: string } | null {
  if (read.kind === "absent") return { status: "absent", detail: read.detail };
  if (read.kind === "present" && INSTANCE_ENDED.has(read.status)) {
    return { status: read.status, detail: `its Workflow instance is ${read.status}` };
  }
  return null;
}

// ---------------------------------------------------------------- the sweep ---

/**
 * How old a live record must be before the sweep asks about it. A submission
 * writes the run row BEFORE it creates the Workflow instance, so a brand-new
 * record genuinely has no instance for a moment; the sweep must never mistake
 * that moment for an ending. Generous, because a stuck record costs an hour
 * and a wrongly finished live run costs the run.
 */
export const ORPHAN_MIN_AGE_MS = 30 * 60_000;

/** Most records one sweep asks about; the rest wait for the next hour. */
export const ORPHAN_SWEEP_LIMIT = 50;

/** What the sweep did with one live record. */
export type OrphanOutcome =
  | {
      outcome: "finished";
      run_id: string;
      project: string;
      from: string;
      state: "stopped" | "failed";
      supervisor: string;
      reason: string;
      lease: string;
      tokens_revoked: number;
    }
  /** `undecided` when nothing could be learned; false when the run is simply live. */
  | { outcome: "left"; run_id: string; reason: string; undecided: boolean };

/** The terminal state a record goes to when its supervisor ended without finishing it. */
export function orphanEndState(recordState: string): "stopped" | "failed" {
  // A stop was asked for and nothing is left to refuse it: the run stopped.
  // Anything else ended without saying how, which is a failure.
  return recordState === "stopping" ? "stopped" : "failed";
}

/**
 * Finishes every live run record whose Workflow instance has certainly
 * ended or is absent. Never throws: a record that cannot be decided is left
 * for the next hour with its reason, and the next record is still swept.
 */
export async function sweepOrphanedRuns(
  env: Env,
  now: Date = new Date(),
  options: { minAgeMs?: number; limit?: number } = {},
): Promise<OrphanOutcome[]> {
  const binding = runWorkflowBinding(env);
  if (binding === null) return [];
  const minAge = options.minAgeMs ?? ORPHAN_MIN_AGE_MS;
  const cutoff = new Date(now.getTime() - minAge).toISOString();
  const placeholders = ACTIVE_RUN_STATES.map(() => "?").join(", ");
  const rows = await env.DB.prepare(
    `SELECT run_id, project, epic, base_sha, requested_by, state,
            started_at, ended_at, cost_usd, cost_source, trace_id, credential_grade
     FROM runs
     WHERE state IN (${placeholders}) AND started_at <= ?
     ORDER BY started_at ASC, run_id ASC
     LIMIT ?`,
  )
    .bind(...ACTIVE_RUN_STATES, cutoff, options.limit ?? ORPHAN_SWEEP_LIMIT)
    .all<Run>();

  const outcomes: OrphanOutcome[] = [];
  for (const run of rows.results) {
    try {
      outcomes.push(await sweepOne(env, binding, run, now));
    } catch (error) {
      outcomes.push({
        outcome: "left",
        run_id: run.run_id,
        reason: `the sweep failed on this record: ${String(error)}`,
        undecided: true,
      });
    }
  }
  return outcomes;
}

async function sweepOne(
  env: Env,
  binding: RunWorkflowBinding,
  run: Run,
  now: Date,
): Promise<OrphanOutcome> {
  const read = await readRunInstance(binding, run.run_id);
  const ended = endedSupervisorOf(read);
  if (ended === null) {
    return {
      outcome: "left",
      run_id: run.run_id,
      reason: read.kind === "present" ? `its Workflow instance is ${read.status}` : read.detail,
      undecided: read.kind !== "present",
    };
  }

  const state = orphanEndState(run.state);
  const endedAt = now.toISOString();
  const reason =
    `orphaned: the record said ${run.state} but ${ended.detail}, ` +
    `so nothing would ever finish it; the factory's sweep finished it as ${state}`;

  // The flip is conditional on the record still being the live one the sweep
  // decided on, so a finalize that landed meanwhile wins and nothing below
  // runs twice for one ending.
  const flipped = await env.DB.prepare(
    "UPDATE runs SET state = ?, ended_at = COALESCE(ended_at, ?) WHERE run_id = ? AND state = ?",
  )
    .bind(state, endedAt, run.run_id, run.state)
    .run();
  if ((flipped.meta.changes ?? 0) === 0) {
    const current = await getRun(env.DB, run.run_id);
    return {
      outcome: "left",
      run_id: run.run_id,
      reason: `the record moved to ${current?.state ?? "absent"} while it was swept`,
      undecided: false,
    };
  }

  // The credential next, as every other ending does: nothing may still spend
  // on a run whose record says it is over.
  const tokensRevoked = await revokeRunTokens(
    env,
    run.run_id,
    `orphaned:supervisor-${ended.status}`,
  ).catch((error: unknown) => {
    console.error(
      `factory orphans: ${run.run_id} could not revoke its gateway tokens: ${String(error)}`,
    );
    return 0;
  });

  // The reason beside the state, where `status` reads a finished run's
  // verdict from, and the dispatch log's closing line.
  await recordRunProgress(
    env.DB,
    run.run_id,
    { progress: "unknown", detail: reason },
    endedAt,
  ).catch((error: unknown) => {
    console.error(`factory orphans: ${run.run_id} could not stamp its reason: ${String(error)}`);
  });
  await logDispatch(env, {
    run_id: run.run_id,
    epic: run.epic,
    decision: `finished:${state}`,
    reason: null,
  }).catch((error: unknown) => {
    console.error(`factory orphans: ${run.run_id} could not log its finish: ${String(error)}`);
  });

  // The lease last: releasing it may ignite a parked submission, which must
  // find this record already terminal.
  let lease: string;
  try {
    lease = (await roomFor(env, run.project).releaseLeaseOfEndedRun(run.run_id)).detail;
  } catch (error) {
    lease = `the lease could not be released (it expires on its own): ${String(error)}`;
  }

  return {
    outcome: "finished",
    run_id: run.run_id,
    project: run.project,
    from: run.state,
    state,
    supervisor: ended.status,
    reason,
    lease,
    tokens_revoked: tokensRevoked,
  };
}
