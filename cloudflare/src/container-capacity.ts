/**
 * The account's container slots: who is holding them, and giving them back
 * (hn6's cloud run run_8511bc66…, 2026-09-30).
 *
 * ## What happened
 *
 * Every container this factory boots — a run's orchestrator and each of its
 * workers — is one instance of the one `[[containers]]` application, and the
 * account runs at most `max_instances` (mirrored as FACTORY_MAX_INSTANCES,
 * tick b6e) of them at once. The run's orchestrator plus two workers held all
 * three. Starting a third worker through the dispatch door addressed its
 * container, the platform queued it for a slot that could not free, and the
 * door sat inside that wait until the orchestrator's client timed out —
 * twelve times, each read as `remote_transient`, until the run's continuation
 * cap was spent and it failed. Then nothing destroyed its two workers: after
 * the run was over they were still `running`, still holding two of the three.
 *
 * ## What this module is
 *
 *  - **The count** ({@link heldSlots}): the live runs' orchestrators plus
 *    every worker container booted (migration 0018) and neither settled
 *    (0019 — the door reclaims a settled worker at its first terminal
 *    observation) nor reclaimed (0021). The door asks it before any FRESH
 *    boot and answers `503 no_capacity` at once when the account is full
 *    (sandbox-dispatch.ts), so a start never waits in the platform's queue;
 *    the orchestrator treats that answer as a wait, never as a transient
 *    remote, and never spends a continuation on it. An ADOPTION needs no slot
 *    and is never refused for one.
 *  - **The reclaim** ({@link reclaimRunWorkers}): every worker container of a
 *    run that is over is asked to stop and push (`ticks-worker --cancel`,
 *    tick 7zk — its own salvage, report and push run), given a grace window,
 *    then destroyed, and each reclaim is RECORDED. finalize does it for every
 *    ending of a run — completed, failed, stopped, the orchestrator dead —
 *    and the hourly cron ({@link reclaimOrphanedWorkers}) does it for any
 *    container whose run is not live, which is what a finalize that never ran
 *    (or ran before this code existed) leaves behind.
 *
 * Asking a container anything boots it when it is not running (the SDK
 * starts a container on any call), and a boot waits for a slot. So the
 * reclaim addresses a container's processes only while its boot is recent
 * enough for it to be up (RECLAIM_LOOKBACK_MS), and every such question is
 * bounded; anything older, or anything that does not answer in time, is
 * destroyed without being asked — `destroy` never boots one.
 */

import { ACTIVE_RUN_STATES } from "./runs";
import type { OrchestratorSandbox, SandboxBinding } from "./sandbox";
import { attemptSandboxNameForSlot } from "./sandbox-executor";
import { WORKER_COMMAND, workerCancelCommand } from "./worker-boot";
import { defaultSleeper, type Sleeper } from "./worker-dispatch";

// ------------------------------------------------------------- the ceiling ---

/**
 * The ceiling when the deployment states none: `[[containers]] max_instances`
 * as wrangler.toml ships it. A deployment always states FACTORY_MAX_INSTANCES
 * (the deploy refuses a config without it), so this is a floor for a
 * misconfiguration, not a choice.
 */
export const DEFAULT_FACTORY_MAX_INSTANCES = 12;

/** The account's container ceiling, from the deployment's mirror of it. */
export function factoryMaxInstances(env: { FACTORY_MAX_INSTANCES?: string }): number {
  const parsed = Number.parseInt(env.FACTORY_MAX_INSTANCES ?? "", 10);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : DEFAULT_FACTORY_MAX_INSTANCES;
}

/**
 * How far back a boot can still be holding a container. A worker's wall is
 * bounded by the run's (RUN_MAX_WALL_CLOCK_MS, a day at most), and an
 * unwatched worker container idles out after SANDBOX_SLEEP_AFTER; a boot
 * older than this is not counted, and is destroyed without being asked.
 */
export const RECLAIM_LOOKBACK_MS = 24 * 60 * 60 * 1000;

// ---------------------------------------------------------------- the count ---

/** Who holds the account's slots, as the door counts them. */
export type HeldSlots = {
  /** One per live container-orchestrated run: its orchestrator's container. */
  orchestrators: number;
  /** Worker containers booted and neither settled nor reclaimed. */
  workers: number;
  held: number;
  max: number;
};

/** Counts the slots held, against the deployment's ceiling. */
export async function heldSlots(
  db: D1Database,
  env: { FACTORY_MAX_INSTANCES?: string },
  now: Date = new Date(),
): Promise<HeldSlots> {
  const since = new Date(now.getTime() - RECLAIM_LOOKBACK_MS).toISOString();
  const orchestrators = await db
    .prepare(
      // A locally orchestrated run (`ticfac run --cloud-workers`, migration
      // 0022) holds no container of its own: its orchestrator is the
      // operator's machine, so only its workers are counted, below.
      `SELECT COUNT(*) AS n FROM runs WHERE state IN (${ACTIVE_RUN_STATES.map(() => "?").join(", ")})
         AND NOT EXISTS (SELECT 1 FROM run_orchestrator o WHERE o.run_id = runs.run_id
           AND o.kind = 'local')`,
    )
    .bind(...ACTIVE_RUN_STATES)
    .first<{ n: number }>();
  const workers = await db
    .prepare(
      `SELECT COUNT(*) AS n FROM sandbox_job_boot b WHERE b."at" >= ?
         AND NOT EXISTS (SELECT 1 FROM sandbox_job_settled s WHERE s.run_id = b.run_id
           AND s.tick_id = b.tick_id AND s.attempt = b.attempt AND s.job = b.job)
         AND NOT EXISTS (SELECT 1 FROM sandbox_job_reclaimed r WHERE r.run_id = b.run_id
           AND r.tick_id = b.tick_id AND r.attempt = b.attempt AND r.job = b.job)`,
    )
    .bind(since)
    .first<{ n: number }>();
  const o = orchestrators?.n ?? 0;
  const w = workers?.n ?? 0;
  return { orchestrators: o, workers: w, held: o + w, max: factoryMaxInstances(env) };
}

/**
 * Whether the named job has a container this identity could ADOPT: booted,
 * and neither settled nor reclaimed. A start under such an identity needs no
 * new slot — the door adopts the live work process if there is one — so the
 * capacity refusal never stands in its way.
 */
export async function mayBeAdoptable(
  db: D1Database,
  key: { run_id: string; tick_id: string; attempt: number; job: string },
): Promise<boolean> {
  const row = await db
    .prepare(
      `SELECT 1 AS live FROM sandbox_job_boot b
       WHERE b.run_id = ? AND b.tick_id = ? AND b.attempt = ? AND b.job = ?
         AND NOT EXISTS (SELECT 1 FROM sandbox_job_settled s WHERE s.run_id = b.run_id
           AND s.tick_id = b.tick_id AND s.attempt = b.attempt AND s.job = b.job)
         AND NOT EXISTS (SELECT 1 FROM sandbox_job_reclaimed r WHERE r.run_id = b.run_id
           AND r.tick_id = b.tick_id AND r.attempt = b.attempt AND r.job = b.job)`,
    )
    .bind(key.run_id, key.tick_id, key.attempt, key.job)
    .first<{ live: number }>();
  return row !== null;
}

// -------------------------------------------------------------- the reclaim ---

/** One job boot that may still hold a container. */
type JobBoot = {
  run_id: string;
  tick_id: string;
  attempt: number;
  job: string;
  at: string;
  settled: boolean;
  /**
   * Already recorded reclaimed — re-checked by the sweep, because a destroy
   * that resolved is not proof the container went (hn6: two wedged workers
   * were recorded reclaimed and still listed `running` an hour later).
   */
  reclaimed: boolean;
};

/** Booted and never reclaimed — settled ones too: a failed destroy wants another. */
async function unreclaimedBoots(
  db: D1Database,
  filter: { run_id?: string; orphaned?: boolean; limit?: number; recheckSince?: string },
): Promise<JobBoot[]> {
  const binds: unknown[] = [];
  // Never reclaimed — or, for the sweep, reclaimed recently enough that the
  // container might have outlived its destroy.
  const where = [
    filter.recheckSince === undefined
      ? `NOT EXISTS (SELECT 1 FROM sandbox_job_reclaimed r WHERE r.run_id = b.run_id
           AND r.tick_id = b.tick_id AND r.attempt = b.attempt AND r.job = b.job)`
      : `NOT EXISTS (SELECT 1 FROM sandbox_job_reclaimed r WHERE r.run_id = b.run_id
           AND r.tick_id = b.tick_id AND r.attempt = b.attempt AND r.job = b.job AND r."at" < ?)`,
  ];
  if (filter.recheckSince !== undefined) binds.push(filter.recheckSince);
  if (filter.run_id !== undefined) {
    where.push("b.run_id = ?");
    binds.push(filter.run_id);
  }
  if (filter.orphaned === true) {
    // A run the index does not know, or one that is not live.
    where.push(
      `(runs.state IS NULL OR runs.state NOT IN (${ACTIVE_RUN_STATES.map(() => "?").join(", ")}))`,
    );
    binds.push(...ACTIVE_RUN_STATES);
  }
  binds.push(filter.limit ?? 200);
  const rows = await db
    .prepare(
      `SELECT b.run_id, b.tick_id, b.attempt, b.job, b."at" AS at,
         EXISTS (SELECT 1 FROM sandbox_job_settled s WHERE s.run_id = b.run_id
           AND s.tick_id = b.tick_id AND s.attempt = b.attempt AND s.job = b.job) AS settled,
         EXISTS (SELECT 1 FROM sandbox_job_reclaimed rc WHERE rc.run_id = b.run_id
           AND rc.tick_id = b.tick_id AND rc.attempt = b.attempt AND rc.job = b.job) AS reclaimed
       FROM sandbox_job_boot b LEFT JOIN runs ON runs.run_id = b.run_id
       WHERE ${where.join(" AND ")}
       ORDER BY b."at" DESC LIMIT ?`,
    )
    .bind(...binds)
    .all<Omit<JobBoot, "settled" | "reclaimed"> & { settled: number; reclaimed: number }>();
  return rows.results.map((row) => ({
    ...row,
    settled: row.settled === 1,
    reclaimed: row.reclaimed === 1,
  }));
}

/** One reclaim, as recorded (migration 0021). */
export type SandboxJobReclaim = {
  run_id: string;
  tick_id: string;
  attempt: number;
  job: string;
  /** The container's name. */
  sandbox: string;
  reason: string;
  /** A live work process was asked to stop and push before the destroy. */
  salvaged: boolean;
  detail: string;
  at: string;
};

async function recordReclaim(db: D1Database, reclaim: SandboxJobReclaim): Promise<void> {
  await db
    .prepare(
      `INSERT OR REPLACE INTO sandbox_job_reclaimed
        (run_id, tick_id, attempt, job, reason, salvaged, detail, "at")
       VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
    )
    .bind(
      reclaim.run_id,
      reclaim.tick_id,
      reclaim.attempt,
      reclaim.job,
      reclaim.reason,
      reclaim.salvaged ? 1 : 0,
      reclaim.detail,
      reclaim.at,
    )
    .run();
}

/** Every reclaim recorded for one run. */
export async function listReclaims(
  db: D1Database,
  runID: string,
): Promise<Omit<SandboxJobReclaim, "sandbox">[]> {
  const rows = await db
    .prepare(
      `SELECT run_id, tick_id, attempt, job, reason, salvaged, detail, "at" AS at
       FROM sandbox_job_reclaimed WHERE run_id = ? ORDER BY tick_id, attempt, job`,
    )
    .bind(runID)
    .all<Omit<SandboxJobReclaim, "sandbox" | "salvaged"> & { salvaged: number }>();
  return rows.results.map((row) => ({ ...row, salvaged: row.salvaged === 1 }));
}

/** How long a reclaim waits for asked workers to push before it destroys them. */
export const DEFAULT_RECLAIM_GRACE_MS = 60_000;
const RECLAIM_POLL_MS = 2_000;
/** How long one question to a container may take before it is destroyed unasked. */
const RECLAIM_ASK_TIMEOUT_MS = 15_000;

export type ReclaimOptions = {
  /** Why: `run_ended:<state>` from finalize, `run_not_live` from the sweep. */
  reason: string;
  graceMs?: number;
  pollMs?: number;
  sleep?: Sleeper;
  now?: () => Date;
  /** Bound one container question; a test shortens it. */
  askTimeoutMs?: number;
};

/** A question that does not answer in time is no answer — never a hang. */
async function bounded<T>(what: Promise<T>, ms: number): Promise<T | "timeout"> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const expired = new Promise<"timeout">((resolve) => {
    timer = setTimeout(() => resolve("timeout"), ms);
  });
  try {
    return await Promise.race([what, expired]);
  } finally {
    if (timer !== null) clearTimeout(timer);
  }
}

/** The live work process in a container, if it has one. */
function liveWork(listed: { id: string; state: string; command?: string }[]): string | null {
  for (const view of listed) {
    if (view.command === WORKER_COMMAND && view.state === "running") return view.id;
  }
  return null;
}

/**
 * Reclaims the worker containers of the named boots: ask every live one to
 * stop and push, wait out the grace for the asked ones together, destroy them
 * all, and record each. Never throws: a container that cannot be asked is
 * destroyed unasked, and one that cannot be destroyed is left unrecorded so
 * the next sweep tries again.
 */
async function reclaimBoots(
  db: D1Database,
  binding: SandboxBinding,
  boots: JobBoot[],
  options: ReclaimOptions,
): Promise<SandboxJobReclaim[]> {
  const sleep = options.sleep ?? defaultSleeper;
  const now = options.now ?? (() => new Date());
  const graceMs = Math.max(options.graceMs ?? DEFAULT_RECLAIM_GRACE_MS, 0);
  const pollMs = Math.max(options.pollMs ?? RECLAIM_POLL_MS, 1);
  const askTimeout = options.askTimeoutMs ?? RECLAIM_ASK_TIMEOUT_MS;
  const recentSince = now().getTime() - RECLAIM_LOOKBACK_MS;

  type Pending = {
    boot: JobBoot;
    name: string;
    sandbox: OrchestratorSandbox | null;
    work: string | null;
    salvaged: boolean;
    detail: string;
  };
  const pending: Pending[] = [];

  // 1. Ask. A settled job's container was reclaimed at settlement, and an
  //    old boot's has idled out: neither is asked anything (asking would boot
  //    it), both are destroyed below as the backstop.
  for (const boot of boots) {
    const name = attemptSandboxNameForSlot(boot.run_id, boot.tick_id, boot.attempt, boot.job);
    const entry: Pending = { boot, name, sandbox: null, work: null, salvaged: false, detail: "" };
    pending.push(entry);
    try {
      entry.sandbox = await binding.get(name);
    } catch (error) {
      entry.detail = `the container could not be addressed: ${String(error)}`;
      continue;
    }
    if (boot.reclaimed) {
      // A re-check: only a container that still reads as up is reclaimed
      // again; one that went is not addressed any further.
      const up =
        entry.sandbox.isRunning === undefined
          ? false
          : await bounded(entry.sandbox.isRunning(), askTimeout).catch(() => false);
      if (up !== true) {
        pending.pop();
        continue;
      }
      entry.detail = "recorded reclaimed, but the container was still running; ";
    }
    if (boot.settled) {
      entry.detail += "the job had settled; its container is destroyed as a backstop";
      continue;
    }
    if (Date.parse(boot.at) < recentSince) {
      entry.detail += "booted too long ago to be running; destroyed without being asked";
      continue;
    }
    try {
      // A container that is not up has nothing to push, and asking it for its
      // processes would start it just to be destroyed.
      if (entry.sandbox.isRunning !== undefined) {
        const up = await bounded(entry.sandbox.isRunning(), askTimeout);
        if (up === false) {
          entry.detail += "the container was not running; destroyed without being asked";
          continue;
        }
      }
      const listed = await bounded(entry.sandbox.listProcesses(), askTimeout);
      if (listed === "timeout") {
        entry.detail += "the container did not answer in time; destroyed without being asked";
        continue;
      }
      entry.work = liveWork(listed);
      if (entry.work === null) {
        entry.detail += "no work process was running; nothing to push";
        continue;
      }
      await entry.sandbox.startProcess(workerCancelCommand(options.reason), { env: {} });
      entry.salvaged = true;
      entry.detail += "the live worker was asked to stop and push";
    } catch (error) {
      entry.detail += `the container could not be asked to stop and push: ${String(error)}`;
    }
  }

  // 2. Grace, once for all the asked: each worker's own salvage commits,
  //    reports and pushes; the window ends early when every one is done.
  const asked = pending.filter((entry) => entry.salvaged && entry.work !== null);
  let waited = 0;
  while (asked.length > 0 && waited < graceMs) {
    let running = 0;
    for (const entry of asked) {
      try {
        const view = await entry.sandbox!.getProcess(entry.work!);
        if (view !== null && view.state === "running") running += 1;
      } catch {
        // A container that cannot be asked is not waited for.
      }
    }
    if (running === 0) break;
    await sleep(Math.min(pollMs, graceMs - waited));
    waited += Math.min(pollMs, graceMs - waited);
  }
  for (const entry of asked) {
    try {
      const view = await entry.sandbox!.getProcess(entry.work!);
      entry.detail +=
        view !== null && view.state === "running"
          ? `; still running ${graceMs}ms later, destroyed with anything it had not pushed`
          : "; it finished within the grace window";
    } catch {
      entry.detail += "; its state could not be read before the destroy";
    }
  }

  // 3. Destroy, and record.
  const done: SandboxJobReclaim[] = [];
  for (const entry of pending) {
    if (entry.sandbox === null) {
      console.error(`factory reclaim: ${entry.name} was not reclaimed: ${entry.detail}`);
      continue;
    }
    try {
      const destroyed = await bounded(entry.sandbox.destroy(), askTimeout);
      if (destroyed === "timeout") throw new Error("the destroy did not return in time");
    } catch (error) {
      console.error(`factory reclaim: could not destroy ${entry.name}: ${String(error)}`);
      continue;
    }
    // A destroy that returned is not yet a container that went: one still
    // reading as up is left unrecorded, so the next sweep tries again.
    if (entry.sandbox.isRunning !== undefined) {
      const up = await bounded(entry.sandbox.isRunning(), askTimeout).catch(
        () => "timeout" as const,
      );
      if (up === true) {
        console.error(
          `factory reclaim: ${entry.name} still reads as running after its destroy; ` +
            "left unrecorded for the next sweep",
        );
        continue;
      }
    }
    const reclaim: SandboxJobReclaim = {
      run_id: entry.boot.run_id,
      tick_id: entry.boot.tick_id,
      attempt: entry.boot.attempt,
      job: entry.boot.job,
      sandbox: entry.name,
      reason: options.reason,
      salvaged: entry.salvaged,
      detail: entry.detail,
      at: now().toISOString(),
    };
    try {
      await recordReclaim(db, reclaim);
    } catch (error) {
      console.error(`factory reclaim: could not record ${entry.name}'s reclaim: ${String(error)}`);
    }
    console.log(
      `factory reclaim: ${entry.name} (${options.reason}) — ${entry.detail}` +
        (entry.salvaged ? " [salvaged]" : ""),
    );
    done.push(reclaim);
  }
  return done;
}

/**
 * Reclaims every worker container one run booted and has not had reclaimed —
 * finalize's call, for every ending of a run. Never throws.
 */
export async function reclaimRunWorkers(
  db: D1Database,
  binding: SandboxBinding | null,
  runID: string,
  options: ReclaimOptions,
): Promise<SandboxJobReclaim[]> {
  if (binding === null) return [];
  try {
    const boots = await unreclaimedBoots(db, { run_id: runID });
    return await reclaimBoots(db, binding, boots, options);
  } catch (error) {
    console.error(`factory reclaim: ${runID}'s workers could not be reclaimed: ${String(error)}`);
    return [];
  }
}

/**
 * The hourly sweep: reclaims every worker container whose run is not live —
 * a run whose finalize never reached its workers, or a run that ended before
 * finalize reclaimed any. A live run's workers are its own and untouched.
 * Never throws.
 */
export async function reclaimOrphanedWorkers(
  db: D1Database,
  binding: SandboxBinding | null,
  options: Omit<ReclaimOptions, "reason"> = {},
): Promise<SandboxJobReclaim[]> {
  if (binding === null) return [];
  try {
    const recheckSince = new Date(Date.now() - RECLAIM_LOOKBACK_MS).toISOString();
    const orphaned = await unreclaimedBoots(db, { orphaned: true, recheckSince });
    return await reclaimBoots(db, binding, orphaned, { ...options, reason: "run_not_live" });
  } catch (error) {
    console.error(`factory reclaim: the sweep could not run: ${String(error)}`);
    return [];
  }
}
