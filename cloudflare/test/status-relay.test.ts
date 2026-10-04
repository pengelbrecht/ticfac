import { env, SELF } from "cloudflare:test";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { insertRun } from "../src/db";
import { issueRunToken, issueWorkerRunToken } from "../src/gateway";
import { STATUS_RELAY_PATH } from "../src/status-relay";

/**
 * The status-relay door (hn6, tick h7w): `POST /api/status-relay`, the
 * run-credential door a CLOUD run's own orchestrator container pushes its
 * status model through — the half that makes the phone page's cloud row the
 * SAME model the terminal's watch renders.
 *
 * These pin the door the Go container pusher talks to, against real workerd
 * with the real D1 tables:
 *
 *  - the credential is the RUN's own, and only the ORCHESTRATOR's: a worker
 *    token names its tick, and a worker never speaks for the run's status;
 *  - the envelope is the one the operator door pins (parseSnapshotEnvelope),
 *    with the host pinned per door: cloud here, local on the operator's;
 *  - a container cannot push for a run it is not — the credential names the
 *    run, and an envelope naming another is refused;
 *  - the push drives the same alert evaluation the operator door drives.
 */

const BASE = "https://factory.example.com";

let operatorToken: string;
const originalHash = env.FACTORY_TOKEN_HASH;

beforeAll(async () => {
  operatorToken = mintFactoryToken();
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(operatorToken);
});

afterAll(() => {
  if (originalHash === undefined) delete env.FACTORY_TOKEN_HASH;
  else env.FACTORY_TOKEN_HASH = originalHash;
});

afterEach(async () => {
  await env.DB.prepare("DELETE FROM status_snapshots").run();
  await env.DB.prepare("DELETE FROM status_alerts").run();
  await env.DB.prepare("DELETE FROM runs").run();
  await env.DB.prepare("DELETE FROM run_gateway_token").run();
});

let counter = 0;

/** One cloud run with its orchestrator's credential, as a container holds it. */
async function cloudRun(
  state: "running" | "failed" = "running",
): Promise<{ runID: string; token: string }> {
  const runID = `run_relay_${++counter}`;
  await insertRun(env.DB, {
    run_id: runID,
    project: "example-org/status-relay",
    epic: "hn6",
    base_sha: "b".repeat(40),
    requested_by: "operator",
    state,
    started_at: new Date().toISOString(),
    ended_at: state === "running" ? null : new Date().toISOString(),
    cost_usd: 0,
    cost_source: null,
    trace_id: null,
    credential_grade: "write",
  });
  const { token } = await issueRunToken(env, { run_id: runID, tick_id: "hn6", attempt: 1 });
  return { runID, token };
}

/** One `ticfac.status.v1` document as the container's run-epic emits it. */
function cloudDoc(runID: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: 1,
    run_id: runID,
    epic_id: "hn6",
    host: "cloud",
    generated_at: new Date().toISOString(),
    degraded: [],
    liveness: { alive: true, state: "alive", reason: "", source: "run.pid" },
    lifecycle: { phase: "waves", phases: [], wave: { active: 1, total: 2 } },
    progress: { ticks: { total: 4, closed: 2, open: 2 }, waves: { total: 2, done: 1, active: 1 } },
    waves: [
      {
        wave: 1,
        state: "active",
        ticks: [
          { tick_id: "h7w", title: "Phone page renders the run's own model", state: "in-flight" },
        ],
      },
    ],
    workers: null,
    waits_on: { kind: "workers", what: "1 in-flight attempt(s)", needs_person: false },
    attention: [],
    health: {
      remote_retries: 0,
      interventions: 0,
      stall_warnings: 0,
      wall_clocks_fired: 0,
      verdict: { state: "healthy", summary: "healthy", recovered: [] },
    },
    gates: [],
    ci: null,
    cost: { recorded_usd: 0.41, attempts: 3, basis: "AI Gateway logs" },
    remaining: null,
    ...extra,
  };
}

function push(token: string, runID: string, doc: Record<string, unknown>): Promise<Response> {
  return SELF.fetch(`${BASE}${STATUS_RELAY_PATH}`, {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify({
      schema_version: 1,
      run_id: runID,
      host: "cloud",
      pushed_at: new Date().toISOString(),
      model: doc,
    }),
  });
}

describe("the orchestrator container's status-relay door", () => {
  it("stores the run's own pushed model, and answers for the storage", async () => {
    const { runID, token } = await cloudRun();
    const res = await push(token, runID, cloudDoc(runID));
    expect(res.status).toBe(201);
    const body = (await res.json()) as { stored: boolean; run_id: string; alerts: unknown };
    expect(body.stored).toBe(true);
    expect(body.run_id).toBe(runID);
    expect(body.alerts).toBeDefined();

    const stored = await env.DB.prepare(
      "SELECT run_id, host FROM status_snapshots WHERE run_id = ?",
    )
      .bind(runID)
      .first<{ run_id: string; host: string }>();
    expect(stored?.host).toBe("cloud");
  });

  it("refuses a worker's credential: only the run's orchestrator speaks for its status", async () => {
    const { runID } = await cloudRun();
    const { token } = await issueWorkerRunToken(env, { run_id: runID, tick_id: "h7w", attempt: 1 });
    const res = await push(token, runID, cloudDoc(runID));
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error: string };
    expect(body.error).toBe("not_orchestrator");
  });

  it("refuses a caller with no run credential at all", async () => {
    const { runID } = await cloudRun();
    const res = await push("not-a-token", runID, cloudDoc(runID));
    expect(res.status).toBe(401);
  });

  it("refuses an envelope naming a run the credential does not", async () => {
    const { runID, token } = await cloudRun();
    const other = "run_somebody_elses";
    const res = await push(token, runID, cloudDoc(other));
    // The parse fails first — the envelope names one run, its model another —
    // so the refusal is the door's own vocabulary either way.
    expect([400, 422]).toContain(res.status);
  });

  it("refuses a credential's push for a run it is not, even with a well-formed model", async () => {
    const { runID, token } = await cloudRun();
    await insertRun(env.DB, {
      run_id: "run_other",
      project: "example-org/status-relay",
      epic: "hn6",
      base_sha: "b".repeat(40),
      requested_by: "operator",
      state: "running",
      started_at: new Date().toISOString(),
      ended_at: null,
      cost_usd: 0,
      cost_source: null,
      trace_id: null,
      credential_grade: "write",
    });
    const res = await push(token, "run_other", cloudDoc("run_other"));
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error: string; detail: string };
    expect(body.detail).toContain(runID);
  });

  it("refuses a LOCAL envelope: this door is the cloud run's, the operator's door the local run's", async () => {
    const { runID, token } = await cloudRun();
    const res = await SELF.fetch(`${BASE}${STATUS_RELAY_PATH}`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify({
        schema_version: 1,
        run_id: runID,
        host: "local",
        pushed_at: new Date().toISOString(),
        model: { ...cloudDoc(runID), host: "local" },
      }),
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as { detail: string };
    expect(body.detail).toContain("cloud");
  });

  it("refuses a run that is over: the run's credential dies with it", async () => {
    // The gateway's own rule (run_not_active): a container whose run ended
    // cannot push anything, so the only pushes this door takes are a live
    // run's — which includes the container's own ENDING push, made before the
    // Workflow turns the run row terminal. A model that survives past that
    // is the page's stopped-clock case, answered there by the run row.
    const { runID, token } = await cloudRun("failed");
    const res = await push(token, runID, cloudDoc(runID));
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error: string };
    expect(body.error).toBe("run_not_active");
  });

  it("takes GET for nothing: the door answers POST", async () => {
    const res = await SELF.fetch(`${BASE}${STATUS_RELAY_PATH}`, { method: "GET" });
    expect(res.status).toBe(405);
  });

  it("is exempt from the operator bearer: a container holds no operator token, and one changes nothing", async () => {
    // The operator's own token is NOT the way in — the door answers the run's
    // credential, and an operator token is simply an unknown run credential.
    const { runID } = await cloudRun();
    const res = await push(operatorToken, runID, cloudDoc(runID));
    expect(res.status).toBe(401);
  });

  it("refuses the operator's door to a cloud envelope: the doors scope their callers", async () => {
    const { runID } = await cloudRun();
    const res = await SELF.fetch(`${BASE}/api/status-snapshots`, {
      method: "POST",
      headers: { authorization: `Bearer ${operatorToken}`, "content-type": "application/json" },
      body: JSON.stringify({
        schema_version: 1,
        run_id: runID,
        host: "cloud",
        pushed_at: new Date().toISOString(),
        model: cloudDoc(runID),
      }),
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as { detail: string };
    expect(body.detail).toContain("local");
  });
});
