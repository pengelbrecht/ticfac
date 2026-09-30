import { env, SELF } from "cloudflare:test";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { heldSlots } from "../src/container-capacity";
import { listRunGatewayTokens } from "../src/db";
import { issueRunToken, issueWorkerRunToken } from "../src/gateway";
import {
  HEARTBEAT_PATH,
  heartbeatStale,
  isLocalOrchestrator,
  lastHeartbeatMs,
} from "../src/local-orchestrator";
import { type RunWorkflowInstance, type RunWorkflowParams, roomFor } from "../src/runs";

/**
 * Local orchestrator, cloud workers (`ticfac run <epic> --cloud-workers`):
 * the factory half, at its routes — the submission that marks the run, the
 * operator-authenticated route that hands the run token to the operator's
 * machine, the run-token heartbeat door, and the capacity count that does not
 * charge the run a container it never boots.
 *
 * Real workerd, real D1 and RunRoom; the Workflow binding is a recorder, the
 * way run-routes.test.ts drives the submit path (the Workflow's own local
 * pass is exercised in run-workflow.test.ts).
 */

const BASE = "https://factory.example.com";

class RecordingWorkflow {
  created: { id: string; params: RunWorkflowParams }[] = [];
  async create(options: { id?: string; params?: RunWorkflowParams }): Promise<RunWorkflowInstance> {
    const id = options.id ?? crypto.randomUUID();
    this.created.push({ id, params: options.params! });
    return this.get(id);
  }
  async get(id: string): Promise<RunWorkflowInstance> {
    return {
      id,
      async status() {
        return { status: "running" };
      },
      async sendEvent() {},
    };
  }
}

let workflow: RecordingWorkflow;
let token: string;
const saved: Record<string, unknown> = {};

beforeAll(async () => {
  token = mintFactoryToken();
  for (const name of [
    "FACTORY_TOKEN_HASH",
    "AI_GATEWAY_BASE_URL",
    "FACTORY_BASE_URL",
    "RUN_WORKFLOW",
    "FACTORY_MAX_INSTANCES",
  ]) {
    saved[name] = (env as unknown as Record<string, unknown>)[name];
  }
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
});

afterAll(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
  }
});

beforeEach(() => {
  workflow = new RecordingWorkflow();
  (env as unknown as Record<string, unknown>).RUN_WORKFLOW = workflow;
  env.AI_GATEWAY_BASE_URL = "https://gateway.ai.cloudflare.com/v1/account/ticks";
  env.FACTORY_BASE_URL = BASE;
  (env as unknown as Record<string, unknown>).FACTORY_MAX_INSTANCES = "3";
});

const operator = (): Record<string, string> => ({ Authorization: `Bearer ${token}` });

function post(path: string, body: unknown, headers: Record<string, string>) {
  return SELF.fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { ...headers, "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

let projectCounter = 0;
async function enrolled(): Promise<string> {
  const project = `ticks-test/local-orch-${projectCounter++}`;
  const res = await post("/api/projects", { project, requested_by: "operator" }, operator());
  expect(res.status).toBe(201);
  return project;
}

const SHA = "3e15bff81cd888e82dfe521c507a46f4ddf6913b";

async function submit(project: string, extra: Record<string, unknown> = {}) {
  return post(
    "/api/runs",
    { project, epic: "hn6", base_sha: SHA, requested_by: "operator", ...extra },
    operator(),
  );
}

async function submittedLocal(): Promise<{ runID: string; project: string }> {
  const project = await enrolled();
  const res = await submit(project, { orchestrator: "local" });
  expect(res.status).toBe(201);
  const body = (await res.json()) as { run: { run_id: string } };
  return { runID: body.run.run_id, project };
}

type Credential = {
  run_id: string;
  project: string;
  epic: string;
  token: string;
  factory_max_instances: number;
};

describe("a locally orchestrated run (ticfac run --cloud-workers)", () => {
  it("is marked at submission, carried to the Workflow, and holds its lease as local", async () => {
    const { runID, project } = await submittedLocal();

    expect(await isLocalOrchestrator(env.DB, runID)).toBe(true);
    // The Workflow is told, so it boots no orchestrator container.
    expect(workflow.created.at(-1)?.params.orchestrator).toBe("local");
    // The arbiter is the operator's machine, and a refusal says so.
    await expect(roomFor(env, project).leaseStatus()).resolves.toMatchObject({
      run_id: runID,
      origin: "local",
    });
  });

  it("leaves every other submission a container run, as every run before it was", async () => {
    const project = await enrolled();
    const res = await submit(project);
    expect(res.status).toBe(201);
    const { run } = (await res.json()) as { run: { run_id: string } };
    expect(await isLocalOrchestrator(env.DB, run.run_id)).toBe(false);
    expect(workflow.created.at(-1)?.params.orchestrator).toBeUndefined();
  });

  it("refuses an orchestrator it cannot parse, and a local run asked to queue", async () => {
    const project = await enrolled();
    const bad = await submit(project, { orchestrator: "laptop" });
    expect(bad.status).toBe(400);
    const queued = await submit(project, { orchestrator: "local", queue: true });
    expect(queued.status).toBe(400);
    expect(((await queued.json()) as { detail: string }).detail).toMatch(/cannot be queued/);
  });

  it("hands the operator the run token, and the token beats on the heartbeat door", async () => {
    const { runID } = await submittedLocal();

    // The route is the operator's: without the factory token it is refused
    // before anything is minted.
    const anonymous = await post(`/api/runs/${runID}/orchestrator`, {}, {});
    expect(anonymous.status).toBe(401);

    const res = await post(`/api/runs/${runID}/orchestrator`, {}, operator());
    expect(res.status).toBe(201);
    const credential = (await res.json()) as Credential;
    expect(credential).toMatchObject({ run_id: runID, epic: "hn6", factory_max_instances: 3 });
    expect(credential.token).toMatch(/^tkr_/);
    // The mint is the first heartbeat: a machine that just collected its
    // credential is a machine that is driving the run.
    expect(await lastHeartbeatMs(env.DB, runID)).not.toBeNull();

    const beat = await post(HEARTBEAT_PATH, {}, { Authorization: `Bearer ${credential.token}` });
    expect(beat.status).toBe(200);
    await expect(beat.json()).resolves.toMatchObject({ run_id: runID, stopping: false });

    // The operator's factory token is not a run credential at this door.
    const withOperator = await post(HEARTBEAT_PATH, {}, operator());
    expect(withOperator.status).toBe(401);
  });

  it("re-mints for a restarted machine without cutting off the workers", async () => {
    const { runID } = await submittedLocal();
    const first = (await (
      await post(`/api/runs/${runID}/orchestrator`, {}, operator())
    ).json()) as Credential;
    // A worker the first process started, on its own credential.
    await issueWorkerRunToken(env, { run_id: runID, tick_id: "ltg", attempt: 4 });

    const second = (await (
      await post(`/api/runs/${runID}/orchestrator`, {}, operator())
    ).json()) as Credential;
    expect(second.token).not.toBe(first.token);

    const tokens = await listRunGatewayTokens(env.DB, runID);
    const worker = tokens.find((t) => t.tick_id === "ltg");
    const orchestrators = tokens.filter((t) => t.tick_id === "hn6");
    expect(worker?.revoked_at).toBeNull();
    expect(orchestrators).toHaveLength(2);
    expect(orchestrators.filter((t) => t.revoked_at === null)).toHaveLength(1);

    // The old process's token beats no more.
    const stale = await post(HEARTBEAT_PATH, {}, { Authorization: `Bearer ${first.token}` });
    expect(stale.status).toBe(403);
  });

  it("mints nothing for a container-orchestrated run, and its token has no heartbeat", async () => {
    const project = await enrolled();
    const res = await submit(project);
    const { run } = (await res.json()) as { run: { run_id: string } };

    const refused = await post(`/api/runs/${run.run_id}/orchestrator`, {}, operator());
    expect(refused.status).toBe(409);
    await expect(refused.json()).resolves.toMatchObject({ error: "not_local_orchestrator" });

    const boot = await issueRunToken(env, { run_id: run.run_id, tick_id: "ko8", attempt: 1 });
    const beat = await post(HEARTBEAT_PATH, {}, { Authorization: `Bearer ${boot.token}` });
    expect(beat.status).toBe(409);
  });

  it("is not charged a container slot: its orchestrator is the operator's machine", async () => {
    const before = await heldSlots(env.DB, env);
    await submittedLocal();
    const withLocal = await heldSlots(env.DB, env);
    expect(withLocal.orchestrators).toBe(before.orchestrators);

    const project = await enrolled();
    expect((await submit(project)).status).toBe(201);
    const withContainer = await heldSlots(env.DB, env);
    expect(withContainer.orchestrators).toBe(before.orchestrators + 1);
  });
});

describe("heartbeatStale", () => {
  const minute = 60_000;
  it("counts from the run's start when the machine never beat", () => {
    expect(heartbeatStale({ now_ms: 10 * minute, started_at_ms: 0, heartbeat_ms: null })).toBe(
      false,
    );
    expect(heartbeatStale({ now_ms: 16 * minute, started_at_ms: 0, heartbeat_ms: null })).toBe(
      true,
    );
  });
  it("counts from the last beat once there is one", () => {
    expect(
      heartbeatStale({ now_ms: 30 * minute, started_at_ms: 0, heartbeat_ms: 20 * minute }),
    ).toBe(false);
    expect(
      heartbeatStale({ now_ms: 30 * minute, started_at_ms: 0, heartbeat_ms: 10 * minute }),
    ).toBe(true);
  });
});
