/**
 * The cloud run's half of the status snapshots (hn6, tick h7w): the
 * run-credential door a cloud run's own orchestrator container pushes its
 * status model through — `POST /api/status-relay`.
 *
 * WHY THIS DOOR EXISTS. The phone page is the factory's view of a run, and a
 * CLOUD run's factory can compose a status document from its own records —
 * but that composition is honest about what the factory alone can state,
 * which is not the model's richer half: no health verdict, no tick table,
 * no per-source cost. The terminal's `ticfac watch run_<hex>` builds the
 * full Go model for the same run, so the two renderers disagreed — a verdict,
 * table and cost in the terminal, a bare state chip on the phone (A5/rule 8:
 * two renderers, they cannot disagree). The orchestrator container IS the
 * one place the run's own `ticfac run-epic` runs — the same binary, the same
 * records, the same tracker — so IT gathers the model in situ, exactly the
 * way the local pusher does on a laptop, and pushes it here. The page then
 * renders the run's own model, and the composition remains the fallback for
 * a run that has pushed nothing yet.
 *
 * WHY A RUN-CREDENTIAL DOOR, not the operator's `/api/status-snapshots`: the
 * caller is the orchestrator container, which holds its run's own gateway
 * token and never the operator's — a container must never hold the
 * credential that commands the whole control plane. Beside the other
 * run-credential doors (the feed relay, the heartbeat, the branch claim),
 * for the same reason as each of them.
 *
 * The push is ANSWERED for the storage alone: the alert evaluation that
 * rides it is best-effort by the same rule the operator door holds — a
 * notifier that could fail a status push would make the remote view of a
 * run the victim of the notification about it.
 */

import { STATUS_RELAY_PATH } from "./auth";
import { authorizeGatewayRequest, type GatewayDenial } from "./gateway";
import type { Env } from "./index";
import { evaluateStatusAlerts } from "./notify";
import { parseSnapshotEnvelope, saveStatusSnapshot } from "./status";

export { STATUS_RELAY_PATH };

function refuse(denial: GatewayDenial): Response {
  return Response.json({ error: denial.error, detail: denial.detail }, { status: denial.status });
}

/**
 * `POST /api/status-relay`: the orchestrator container's own status push.
 * The credential names the run — so the envelope must name the same run,
 * and a container cannot push a model for a run it is not. Only the
 * ORCHESTRATOR's credential may push (a worker's token names its tick), and
 * the envelope must be a CLOUD one: this door is the cloud run's, and the
 * operator's door stays the local run's.
 */
export async function statusRelayRoute(request: Request, env: Env): Promise<Response> {
  if (request.method !== "POST") {
    return Response.json(
      { error: "method_not_allowed", detail: "POST" },
      { status: 405, headers: { Allow: "POST" } },
    );
  }
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) return refuse(authorized.denial);
  const { token, run } = authorized;
  if (token.tick_id !== run.epic) {
    return refuse({
      status: 403,
      error: "not_orchestrator",
      detail: `the credential belongs to a worker of ${token.tick_id}; only the run's orchestrator pushes its status model`,
    });
  }

  const raw = await request.text();
  let body: unknown;
  try {
    body = JSON.parse(raw);
  } catch {
    return Response.json(
      { error: "invalid_request", detail: "the snapshot must be a JSON document" },
      { status: 400 },
    );
  }
  const parsed = parseSnapshotEnvelope(body, raw.length);
  if (!parsed.ok) {
    return Response.json({ error: parsed.error, detail: parsed.detail }, { status: parsed.status });
  }
  const envelope = parsed.envelope;
  if (envelope.host !== "cloud") {
    return Response.json(
      {
        error: "invalid_request",
        detail: `this door stores CLOUD runs' snapshots (host "cloud"); a ${envelope.host} run pushes through the operator's door`,
      },
      { status: 400 },
    );
  }
  if (envelope.run_id !== run.run_id) {
    return Response.json(
      {
        error: "invalid_request",
        detail: `the credential names run ${run.run_id}, but the envelope names ${envelope.run_id}`,
      },
      { status: 400 },
    );
  }

  await saveStatusSnapshot(env.DB, {
    run_id: envelope.run_id,
    host: envelope.host,
    epic_id: envelope.model.epic_id,
    pushed_at: envelope.pushed_at,
    model: envelope.model,
    tick_labels: envelope.tick_labels ?? null,
  });

  // The alert evaluation the push drives: per-stop, not per-push (see
  // `notify.ts`). Never fails the push, and never throws out of the try.
  const evaluation = await evaluateStatusAlerts(env, {
    run_id: envelope.run_id,
    doc: envelope.model,
    tick_labels: envelope.tick_labels ?? null,
  }).catch((error: unknown): { sent: number; pending: number; cleared: number } => {
    console.error(
      `factory status: run ${envelope.run_id}'s snapshot was stored but its alerts could not be evaluated: ${String(error)}`,
    );
    return { sent: 0, pending: 0, cleared: 0 };
  });
  return Response.json(
    { stored: true, run_id: envelope.run_id, pushed_at: envelope.pushed_at, alerts: evaluation },
    { status: 201 },
  );
}
