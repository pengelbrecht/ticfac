/**
 * Local orchestrator, cloud workers: a run whose Go orchestrator runs on the
 * operator's machine while this factory boots only its WORKERS.
 *
 * ## Why this exists
 *
 * A container-orchestrated run lives or dies with one Cloudflare container:
 * a rollout replaces it, and every replacement is a state-recovery problem
 * for the orchestrator inside it (hn6's cloud runs). `ticfac run <epic>
 * --cloud-workers` tests the other placement: the reconciler, its git work
 * and its integrated gate on the operator's machine, and every implement and
 * role job in a worker container booted through the per-tick sandbox door —
 * exactly the door a container orchestrator uses, on exactly the same run
 * token. The factory's half of that is small, and all of it is here:
 *
 *   - **The run is marked** (`run_orchestrator`, migration 0022) when a
 *     submission says `orchestrator: "local"`. The mark is what makes the
 *     rest of the factory treat it differently: the Run Workflow boots no
 *     container for it (it supervises the lease, the budgets, the stop and the
 *     orchestrator's heartbeat instead), and the container-capacity count does
 *     not charge it a slot.
 *
 *   - **The credential is handed to the operator**, not to a container:
 *     `POST /api/runs/:id/orchestrator` is authenticated by the operator's
 *     factory token (the default for `/api/*`) and mints the run's own gateway
 *     token for the machine that is going to drive it. The operator's token
 *     never reaches the orchestrator's doors — those still take only the run
 *     token (D17) — and a re-mint (the local process restarted) revokes the
 *     previous ORCHESTRATOR credential only, never a worker's.
 *
 *   - **The orchestrator heartbeats** (`POST /api/heartbeat`, run token): a
 *     container's liveness is the platform's answer about its process; a
 *     laptop's is the laptop saying so. The Workflow ends a run whose
 *     heartbeat went stale ({@link LOCAL_HEARTBEAT_STALE_MS}), which is what
 *     reclaims the workers of an orchestrator that died without a word. The
 *     answer carries the run's state, so a stop requested at the factory
 *     reaches the machine at its next beat.
 */

import { HEARTBEAT_PATH } from "./auth";
import { factoryMaxInstances } from "./container-capacity";
import { getRun } from "./db";
import { authorizeGatewayRequest, type GatewayDenial, issueWorkerRunToken } from "./gateway";
import type { Env } from "./index";
import { ACTIVE_RUN_STATES, type RunState, roomFor } from "./runs";

/** Where a run's orchestrator runs. Absent means the factory's own container. */
export type OrchestratorKind = "container" | "local";

/** The one non-default kind, spelled once. */
export const LOCAL_ORCHESTRATOR: OrchestratorKind = "local";

export { HEARTBEAT_PATH };

/**
 * How long a local orchestrator may go without a heartbeat before the Run
 * Workflow ends its run. The orchestrator beats every minute; fifteen misses
 * is a machine that is gone (crashed, asleep past its keep-awake, off the
 * network), not one that is busy — a busy reconciler still beats, the beat
 * runs on its own goroutine.
 */
export const LOCAL_HEARTBEAT_STALE_MS = 15 * 60 * 1000;

/** Marks a run as locally orchestrated. Idempotent. */
export async function recordLocalOrchestrator(db: D1Database, runID: string): Promise<void> {
  await db
    .prepare(
      `INSERT INTO run_orchestrator (run_id, kind, heartbeat_at) VALUES (?, 'local', NULL)
       ON CONFLICT(run_id) DO NOTHING`,
    )
    .bind(runID)
    .run();
}

/** Whether the run's orchestrator runs on the operator's machine. */
export async function isLocalOrchestrator(db: D1Database, runID: string): Promise<boolean> {
  const row = await db
    .prepare("SELECT kind FROM run_orchestrator WHERE run_id = ?")
    .bind(runID)
    .first<{ kind: string }>();
  return row?.kind === LOCAL_ORCHESTRATOR;
}

/** Records a heartbeat (or the credential's mint, which is the first one). */
export async function recordHeartbeat(db: D1Database, runID: string, at: string): Promise<boolean> {
  const result = await db
    .prepare("UPDATE run_orchestrator SET heartbeat_at = ? WHERE run_id = ? AND kind = 'local'")
    .bind(at, runID)
    .run();
  return (result.meta.changes ?? 0) > 0;
}

/** The last heartbeat, in epoch ms, or null when none was ever recorded. */
export async function lastHeartbeatMs(db: D1Database, runID: string): Promise<number | null> {
  const row = await db
    .prepare("SELECT heartbeat_at FROM run_orchestrator WHERE run_id = ?")
    .bind(runID)
    .first<{ heartbeat_at: string | null }>();
  if (row === null || row.heartbeat_at === null) return null;
  const ms = Date.parse(row.heartbeat_at);
  return Number.isFinite(ms) ? ms : null;
}

/**
 * Whether a local orchestrator has gone quiet: no heartbeat (or credential
 * mint) newer than the bound, counted from the run's own start when it never
 * beat at all — a run whose operator never collected its credential is a run
 * nothing is driving, and it must not hold the project's lease for a day.
 */
export function heartbeatStale(input: {
  now_ms: number;
  started_at_ms: number;
  heartbeat_ms: number | null;
  stale_ms?: number;
}): boolean {
  const last = Math.max(input.started_at_ms, input.heartbeat_ms ?? 0);
  return input.now_ms - last > (input.stale_ms ?? LOCAL_HEARTBEAT_STALE_MS);
}

/**
 * Revokes every live ORCHESTRATOR credential the run holds — the tokens
 * stamped with the epic as their tick — and leaves the workers' alone: a
 * re-mint for a restarted local process must not cut off the worker
 * containers the previous process started, which the new one adopts.
 */
async function revokeOrchestratorTokens(
  db: D1Database,
  runID: string,
  epic: string,
  reason: string,
  at: string,
): Promise<number> {
  const result = await db
    .prepare(
      `UPDATE run_gateway_token SET revoked_at = ?, revoked_reason = ?
       WHERE run_id = ? AND tick_id = ? AND revoked_at IS NULL`,
    )
    .bind(at, reason, runID, epic)
    .run();
  return result.meta.changes ?? 0;
}

function refusal(status: number, error: string, detail: string): Response {
  return Response.json({ error, detail }, { status });
}

/**
 * `POST /api/runs/:id/orchestrator` — the run token for a local orchestrator.
 *
 * Operator-authenticated (the bearer middleware in front of every `/api`
 * route that is not a run-credential door). Refused unless the run is
 * locally orchestrated, still active, and not under a hard stop — a hard stop
 * is a durable refusal to mint (tick gyl), here as at a container boot.
 *
 * The answer carries what the orchestrator's environment needs and nothing
 * the operator does not already hold: the run's identity, the token, and the
 * account's container ceiling — the whole of it is the WORKERS' to use,
 * because this run's orchestrator occupies no container.
 */
export async function orchestratorCredentialRoute(runID: string, env: Env): Promise<Response> {
  const run = await getRun(env.DB, runID);
  if (run === null) return refusal(404, "unknown_run", `no run ${runID}`);
  if (!(await isLocalOrchestrator(env.DB, runID))) {
    return refusal(
      409,
      "not_local_orchestrator",
      `run ${runID} is orchestrated by the factory's own container; its credential is minted at ` +
        "that container's boot and never handed out",
    );
  }
  if (!ACTIVE_RUN_STATES.includes(run.state as RunState)) {
    return refusal(409, "run_not_active", `run ${runID} is ${run.state}; it takes no orchestrator`);
  }
  const stop = await roomFor(env, run.project)
    .stopRequest(runID)
    .catch(() => null);
  if (stop !== null && stop.mode === "hard") {
    return refusal(
      409,
      "run_stopped",
      `a hard stop requested by ${stop.requested_by} at ${stop.requested_at} stands, so no ` +
        "orchestrator credential is minted",
    );
  }
  const at = new Date().toISOString();
  await revokeOrchestratorTokens(env.DB, runID, run.epic, "rotated:local-orchestrator", at);
  // Attempt 1 always: the token's attempt is the feed's boot slot, and a
  // restarted local process appends to the SAME local feed file — so it
  // relays into the same slot, from where that slot stands.
  const credential = await issueWorkerRunToken(env, {
    run_id: runID,
    tick_id: run.epic,
    attempt: 1,
  });
  await recordHeartbeat(env.DB, runID, at);
  return Response.json(
    {
      run_id: run.run_id,
      project: run.project,
      epic: run.epic,
      state: run.state,
      token: credential.token,
      factory_max_instances: factoryMaxInstances(env),
    },
    { status: 201 },
  );
}

/**
 * `POST /api/heartbeat` — a local orchestrator saying it is alive, on its
 * run's own token. The answer is the run's state and whether it is stopping,
 * so a stop asked of the factory reaches the machine at its next beat.
 */
export async function heartbeatRoute(request: Request, env: Env): Promise<Response> {
  if (request.method !== "POST") {
    return Response.json(
      { error: "method_not_allowed", detail: `${HEARTBEAT_PATH} is POST` },
      { status: 405, headers: { allow: "POST" } },
    );
  }
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) {
    const denial: GatewayDenial = authorized.denial;
    return refusal(denial.status, denial.error, denial.detail);
  }
  const run = authorized.run;
  const recorded = await recordHeartbeat(env.DB, run.run_id, new Date().toISOString());
  if (!recorded) {
    return refusal(
      409,
      "not_local_orchestrator",
      `run ${run.run_id} is orchestrated by the factory's own container, which the platform ` +
        "answers for; it has no heartbeat to record",
    );
  }
  return Response.json({
    run_id: run.run_id,
    state: run.state,
    stopping: run.state === "stopping",
  });
}
