import { env, SELF } from "cloudflare:test";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import type { EpicReconcilerParams } from "../src/epic-reconciler";
import { roomFor } from "../src/runs";

/**
 * Tick nu9: the run route drives EpicReconcilerWorkflow.
 *
 * Until this tick, `submitRun` created a RUN_WORKFLOW instance — one
 * orchestrator container agent — for every submission, and the reconciler
 * the whole Phase 4 build delivered (bindings, executor, publisher,
 * close-out) was unreachable from every deployed route. These tests hold the
 * tick's acceptance: a run started through the normal route is DRIVEN by
 * EpicReconcilerWorkflow, and a test FAILS if runs.ts starts RUN_WORKFLOW
 * for an epic run — the `witness` below is that test's teeth: it is bound as
 * RUN_WORKFLOW for this whole file, records every create it is asked for, and
 * the tests here assert it stays empty for epic runs.
 *
 * The fakes are the ONLY substitution (the seam env.d.ts declares): the
 * routes, the RunRoom DO, the lease and D1 are real, so what is proven is
 * the route's wiring, not a mock agreeing with itself.
 */

const BASE = "https://factory.example.com";

type CreatedInstance = { id: string; params: EpicReconcilerParams };

/** The stand-in for the EPIC_RECONCILER binding, recording what the route asks. */
class FakeReconciler {
  created: CreatedInstance[] = [];
  status = "running";

  async create(options: { id?: string; params?: EpicReconcilerParams }): Promise<{
    id: string;
    status: () => Promise<{ status: string }>;
  }> {
    const id = options.id ?? crypto.randomUUID();
    this.created.push({ id, params: options.params! });
    const reconciler = this;
    return {
      id,
      async status() {
        return { status: reconciler.status };
      },
    };
  }

  async get(id: string): Promise<{
    id: string;
    status: () => Promise<{ status: string }>;
    sendEvent?: (event: { type: string }) => Promise<void>;
  }> {
    const reconciler = this;
    return {
      id,
      async status() {
        return { status: reconciler.status };
      },
      async sendEvent(event: { type: string }) {
        expect(event.type).toBe("stop");
      },
    };
  }
}

/**
 * The RUN_WORKFLOW witness. An epic run must never come to it again: if
 * runs.ts starts RUN_WORKFLOW for an epic run, `created` grows and the
 * assertions below fail — the tick's second acceptance clause.
 */
class WitnessWorkflow {
  created: { id: string; params: unknown }[] = [];

  async create(options: { id?: string; params?: unknown }): Promise<{
    id: string;
    status: () => Promise<{ status: string }>;
  }> {
    const id = options.id ?? crypto.randomUUID();
    this.created.push({ id, params: options.params });
    return {
      id,
      async status() {
        return { status: "running" };
      },
    };
  }

  async get(id: string): Promise<{
    id: string;
    status: () => Promise<{ status: string }>;
    sendEvent?: (event: { type: string }) => Promise<void>;
  }> {
    return {
      id,
      async status() {
        return { status: "running" };
      },
    };
  }
}

let reconciler: FakeReconciler;
let witness: WitnessWorkflow;
let token: string;
const originalHash = env.FACTORY_TOKEN_HASH;

beforeAll(async () => {
  token = mintFactoryToken();
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
});

afterAll(() => {
  if (originalHash === undefined) delete env.FACTORY_TOKEN_HASH;
  else env.FACTORY_TOKEN_HASH = originalHash;
  delete env.EPIC_RECONCILER;
  delete env.RUN_WORKFLOW;
  delete env.AI_GATEWAY_BASE_URL;
  delete env.FACTORY_BASE_URL;
});

beforeEach(() => {
  reconciler = new FakeReconciler();
  witness = new WitnessWorkflow();
  env.EPIC_RECONCILER = reconciler as unknown as typeof env.EPIC_RECONCILER;
  env.RUN_WORKFLOW = witness as unknown as typeof env.RUN_WORKFLOW;
  // A submission is refused outright when the deployment has no gateway
  // configured (D17), so a harness that submits runs is a harness with one -
  // and with its own base URL, for the same fail-closed reason.
  env.AI_GATEWAY_BASE_URL = "https://gateway.ai.cloudflare.com/v1/account/ticks";
  env.FACTORY_BASE_URL = BASE;
});

/** Built per call: the token is minted in beforeAll, after this module evaluates. */
const auth = (): Record<string, string> => ({ Authorization: `Bearer ${token}` });

function post(path: string, body?: unknown, headers: Record<string, string> = auth()) {
  return SELF.fetch(`${BASE}${path}`, {
    method: "POST",
    headers,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

let projectCounter = 0;
async function enrolled(name: string): Promise<string> {
  const project = `ticks-test/${name}-${projectCounter++}`;
  const res = await post("/api/projects", { project, requested_by: "operator@example.com" });
  expect(res.status).toBe(201);
  return project;
}

const SHA = "3e15bff81cd888e82dfe521c507a46f4ddf6913b";

function submission(project: string, extra: Record<string, unknown> = {}) {
  return {
    project,
    epic: "ko8",
    base_sha: SHA,
    requested_by: "operator@example.com",
    ...extra,
  };
}

describe("a run started through the normal route is driven by EpicReconcilerWorkflow", () => {
  it("creates the reconciler instance keyed by run id, with the params it drives from", async () => {
    const project = await enrolled("reconciler-driven");

    const res = await post("/api/runs", submission(project));

    expect(res.status).toBe(201);
    const body = (await res.json()) as {
      run: { run_id: string };
      workflow: { id: string; status: string };
    };
    expect(body.workflow.id).toBe(body.run.run_id);

    expect(reconciler.created).toHaveLength(1);
    expect(reconciler.created[0]!.id).toBe(body.run.run_id);
    expect(reconciler.created[0]!.params).toMatchObject({
      run_id: body.run.run_id,
      epic_id: "ko8",
      project,
      // The run branch is the local reconciler's own convention: the
      // integration branch of one epic, `epic/<epic>` — the ref every
      // `.tick/` record and `.ticfac/` state file lives on.
      branch: "epic/ko8",
      base_sha: SHA,
      requested_by: "operator@example.com",
    });
    // The dispatch lease's release credential rides to the driver — the
    // reconciler renews it while the run lives and releases it at the end,
    // exactly as the Run Workflow did (the caller owns it only until
    // ignition; after that the driver does).
    const drivenLease = reconciler.created[0]!.params.lease_token;
    expect(drivenLease).toEqual(expect.any(String));
    // And the lease token is never handed to the submitter.
    expect(JSON.stringify(body)).not.toContain(drivenLease);

    // The project's lease is held by the run the reconciler drives.
    await expect(roomFor(env, project).leaseStatus()).resolves.toMatchObject({
      run_id: body.run.run_id,
      epic: "ko8",
      origin: "cloud",
    });
  });

  it("never starts RUN_WORKFLOW for an epic run — the witness records nothing", async () => {
    const project = await enrolled("witness-empty");

    // The shapes the route can answer with: started, and refused-and-parked.
    const started = await post("/api/runs", submission(project));
    expect(started.status).toBe(201);

    // A second submission for the leased project parks (D22) and later
    // ignites through the RunRoom — the queued path must drive the SAME
    // reconciler, never the container agent.
    const parked = await post(
      "/api/runs",
      submission(project, { epic: "afj", base_sha: `a`.repeat(40), queue: true }),
    );
    expect(parked.status).toBe(202);

    const holder = reconciler.created[0]!;
    const leaseToken = holder.params.lease_token;
    if (leaseToken === undefined) throw new Error("the route must hand the lease to the driver");
    const released = await roomFor(env, project).releaseDispatchLease({
      run_id: holder.id,
      token: leaseToken,
    });
    expect(released.ok).toBe(true);

    // THE acceptance clause: runs.ts must not start RUN_WORKFLOW for an epic
    // run. The witness is bound as RUN_WORKFLOW for this whole file; if the
    // route ever creates a container-agent instance again, this line fails.
    expect(witness.created).toEqual([]);
    // And every instance the route created was the reconciler's: the direct
    // submission, then the queued one the release ignited.
    expect(reconciler.created.length).toBe(2);
    expect(reconciler.created.map((c) => c.params.epic_id)).toEqual(["ko8", "afj"]);
    expect(
      reconciler.created.every(
        (c) => typeof c.params.lease_token === "string" && c.params.lease_token !== "",
      ),
    ).toBe(true);
  });

  it("fails closed at submission when the deployment has no reconciler binding", async () => {
    const project = await enrolled("no-reconciler");
    delete env.EPIC_RECONCILER;

    const res = await post("/api/runs", submission(project));

    expect(res.status).toBe(503);
    const body = (await res.json()) as { error: string; detail: string };
    expect(body.error).toBe("run_unavailable");
    expect(body.detail).toContain("EPIC_RECONCILER");
    // Nothing recorded, nothing leased: a run that cannot be driven is not a
    // run, and the project is not wedged behind it.
    await expect(roomFor(env, project).leaseStatus()).resolves.toBeNull();
  });

  it("answers the run's status from the reconciler instance that drives it", async () => {
    const project = await enrolled("reconciler-status");
    const { run } = (await (await post("/api/runs", submission(project))).json()) as {
      run: { run_id: string };
    };

    const res = await post(`/api/runs/${run.run_id}/stop`, {
      requested_by: "operator@example.com",
    });
    expect(res.status).toBe(200);
    // The stop record is the durable half; the driver notify is the
    // optimisation, and it went to the RECONCILER's instance.
    const body = (await res.json()) as { workflow_notified: boolean };
    expect(body.workflow_notified).toBe(true);

    const status = (await (
      await SELF.fetch(`${BASE}/api/runs/${run.run_id}`, { headers: auth() })
    ).json()) as {
      phase: { workflow: { id: string; status: string } | null };
    };
    expect(status.phase.workflow).toEqual({ id: run.run_id, status: "running" });
  });
});
