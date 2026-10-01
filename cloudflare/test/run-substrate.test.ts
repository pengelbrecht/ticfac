import { env, SELF } from "cloudflare:test";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { heldSlots } from "../src/container-capacity";
import { isLocalOrchestrator } from "../src/local-orchestrator";
import {
  DO_V1,
  INSTANCE_BY_JOB_KIND,
  jobKindOfSandboxName,
  type RunSubstrateRecord,
  readRunSubstrate,
  recordRunSubstrate,
  routedSandboxBinding,
  runIDOfSandboxName,
} from "../src/run-substrate";
import { parseSubmission, type RunWorkflowInstance, type RunWorkflowParams } from "../src/runs";
import {
  type OrchestratorSandbox,
  type SandboxBinding,
  sandboxBinding,
  sandboxName,
} from "../src/sandbox";
import { attemptJobSlot, attemptSandboxName } from "../src/sandbox-executor";

const RUN = "run_0123456789abcdef0123456789abcdef";

/** A binding that records every get it answers, and the options it got. */
function recorder(label: string) {
  const gets: { name: string; options: unknown }[] = [];
  const sandbox = { label } as unknown as OrchestratorSandbox;
  const binding: SandboxBinding = {
    async get(name, options) {
      gets.push({ name, options });
      return sandbox;
    },
  };
  return { binding, gets, sandbox };
}

describe("container names", () => {
  it("name their run before the first dash", () => {
    expect(runIDOfSandboxName(sandboxName(RUN, 2))).toBe(RUN);
    expect(runIDOfSandboxName(attemptSandboxName(RUN, "nmd", 1))).toBe(RUN);
    expect(
      runIDOfSandboxName(attemptSandboxName(RUN, "nmd", 1, `run-${RUN}/tick-nmd/resolve-4-r2`)),
    ).toBe(RUN);
  });

  it("say which job they are for", () => {
    expect(jobKindOfSandboxName(sandboxName(RUN, 1))).toBe("orchestrator");
    expect(jobKindOfSandboxName(attemptSandboxName(RUN, "nmd", 3))).toBe("implement");
    const resolve = `run-${RUN}/tick-nmd/resolve-4-r2`;
    const repair = `run-${RUN}/tick-nmd/repair-1-r3`;
    expect(attemptJobSlot(RUN, "nmd", 3, resolve)).toMatch(/^resolve/);
    expect(jobKindOfSandboxName(attemptSandboxName(RUN, "nmd", 3, resolve))).toBe("resolve");
    expect(jobKindOfSandboxName(attemptSandboxName(RUN, "nmd", 3, repair))).toBe("repair");
  });
});

describe("routedSandboxBinding", () => {
  const records: Record<string, RunSubstrateRecord> = {
    [RUN]: { substrate: DO_V1, image: null },
  };
  const lookup = async (runID: string) =>
    records[runID] ?? { substrate: "sdk0" as const, image: null };

  it("leaves a run with no substrate record on the 0.x binding, options untouched", async () => {
    const legacy = recorder("legacy");
    const v1 = recorder("v1");
    const routed = routedSandboxBinding(legacy.binding, v1.binding, lookup);
    const old = "run_ffffffffffffffffffffffffffffffff";

    await routed.get(attemptSandboxName(old, "abc", 1), { keepAlive: true });
    await routed.get(sandboxName(old, 1));

    expect(v1.gets).toEqual([]);
    expect(legacy.gets).toEqual([
      { name: attemptSandboxName(old, "abc", 1), options: { keepAlive: true } },
      { name: sandboxName(old, 1), options: undefined },
    ]);
  });

  it("routes a do_v1 run's workers to SANDBOXES_V1 at their job's instance size", async () => {
    const legacy = recorder("legacy");
    const v1 = recorder("v1");
    const routed = routedSandboxBinding(legacy.binding, v1.binding, lookup);
    const resolve = attemptSandboxName(RUN, "nmd", 2, `run-${RUN}/tick-nmd/resolve-2`);

    await routed.get(attemptSandboxName(RUN, "nmd", 1), { keepAlive: true });
    await routed.get(resolve);

    expect(legacy.gets).toEqual([]);
    expect(v1.gets).toEqual([
      {
        name: attemptSandboxName(RUN, "nmd", 1),
        options: { keepAlive: true, instance: INSTANCE_BY_JOB_KIND.implement },
      },
      { name: resolve, options: { instance: INSTANCE_BY_JOB_KIND.resolve } },
    ]);
  });

  it("keeps a do_v1 run's orchestrator on 0.x until v1d moves it", async () => {
    const legacy = recorder("legacy");
    const v1 = recorder("v1");
    const routed = routedSandboxBinding(legacy.binding, v1.binding, lookup);

    await routed.get(sandboxName(RUN, 1), { keepAlive: true });

    expect(v1.gets).toEqual([]);
    expect(legacy.gets).toHaveLength(1);
  });

  it("reads a run's record once per binding", async () => {
    let reads = 0;
    const routed = routedSandboxBinding(recorder("l").binding, recorder("v").binding, async (r) => {
      reads += 1;
      return lookup(r);
    });

    await routed.get(attemptSandboxName(RUN, "a", 1));
    await routed.get(attemptSandboxName(RUN, "b", 1));

    expect(reads).toBe(1);
  });
});

describe("the deployment's sandboxBinding", () => {
  it("routes through the run's D1 record", async () => {
    const legacy = recorder("legacy");
    const v1 = recorder("v1");
    const runV1 = `run_${crypto.randomUUID().replaceAll("-", "")}`;
    const run0 = `run_${crypto.randomUUID().replaceAll("-", "")}`;
    await recordRunSubstrate(env.DB, runV1, DO_V1);
    const binding = sandboxBinding({ ...env, SANDBOXES: legacy.binding, SANDBOXES_V1: v1.binding });

    expect(await binding!.get(attemptSandboxName(runV1, "t", 1))).toBe(v1.sandbox);
    expect(await binding!.get(attemptSandboxName(run0, "t", 1))).toBe(legacy.sandbox);
  });

  it("is the 0.x binding alone on a deployment without SANDBOXES_V1", () => {
    const legacy = recorder("legacy");
    expect(sandboxBinding({ ...env, SANDBOXES: legacy.binding, SANDBOXES_V1: undefined })).toBe(
      legacy.binding,
    );
  });

  it("records nothing for a 0.x run, and reads it back as sdk0", async () => {
    const runID = `run_${crypto.randomUUID().replaceAll("-", "")}`;
    await recordRunSubstrate(env.DB, runID, "sdk0");
    expect(await readRunSubstrate(env.DB, runID)).toEqual({ substrate: "sdk0", image: null });
  });
});

describe("parseSubmission: substrate", () => {
  const base = { project: "o/r", epic: "umq", base_sha: "a".repeat(40), requested_by: "op" };

  it("carries do_v1, and leaves the default out", () => {
    const v1 = parseSubmission({ ...base, substrate: "do_v1" });
    const sdk0 = parseSubmission({ ...base, substrate: "sdk0" });
    expect(v1.ok && v1.submission.substrate).toBe("do_v1");
    expect(sdk0.ok && sdk0.submission.substrate).toBeUndefined();
    const none = parseSubmission(base);
    expect(none.ok && none.submission.substrate).toBeUndefined();
  });

  it("refuses an unknown substrate and a queued do_v1 run", () => {
    expect(parseSubmission({ ...base, substrate: "v2" })).toMatchObject({ ok: false });
    expect(parseSubmission({ ...base, substrate: "do_v1", queue: true })).toMatchObject({
      ok: false,
    });
  });
});

// ------------------------------------------------- submission, end to end ---

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

describe("a run submitted on do_v1", () => {
  let token: string;
  const saved: Record<string, unknown> = {};
  const names = ["FACTORY_TOKEN_HASH", "AI_GATEWAY_BASE_URL", "FACTORY_BASE_URL", "RUN_WORKFLOW"];

  beforeAll(async () => {
    token = mintFactoryToken();
    for (const name of names) saved[name] = (env as unknown as Record<string, unknown>)[name];
    env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
  });
  afterAll(() => {
    for (const [name, value] of Object.entries(saved)) {
      if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
      else (env as unknown as Record<string, unknown>)[name] = value;
    }
  });
  beforeEach(() => {
    (env as unknown as Record<string, unknown>).RUN_WORKFLOW = new RecordingWorkflow();
    env.AI_GATEWAY_BASE_URL = "https://gateway.ai.cloudflare.com/v1/account/ticks";
    env.FACTORY_BASE_URL = BASE;
  });

  const post = (path: string, body: unknown) =>
    SELF.fetch(`${BASE}${path}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify(body),
    });

  let counter = 0;
  async function submit(extra: Record<string, unknown>): Promise<string> {
    const project = `ticks-test/substrate-${counter++}`;
    expect((await post("/api/projects", { project, requested_by: "operator" })).status).toBe(201);
    const res = await post("/api/runs", {
      project,
      epic: "umq",
      base_sha: "3e15bff81cd888e82dfe521c507a46f4ddf6913b",
      requested_by: "operator",
      ...extra,
    });
    expect(res.status).toBe(201);
    return ((await res.json()) as { run: { run_id: string } }).run.run_id;
  }

  it("is recorded before its Workflow exists, and a plain run is not", async () => {
    const v1 = await submit({ substrate: "do_v1" });
    const plain = await submit({});

    expect(await readRunSubstrate(env.DB, v1)).toEqual({ substrate: DO_V1, image: null });
    expect(await readRunSubstrate(env.DB, plain)).toEqual({ substrate: "sdk0", image: null });
  });

  it("can be locally orchestrated too (the cloud-workers mode), recording both", async () => {
    const runID = await submit({ substrate: "do_v1", orchestrator: "local" });

    expect(await isLocalOrchestrator(env.DB, runID)).toBe(true);
    expect((await readRunSubstrate(env.DB, runID)).substrate).toBe(DO_V1);
  });

  it("counts its worker containers against the one cap like any other run's", async () => {
    // The count reads boots and runs, never a namespace: a do_v1 worker's
    // boot record is the same row a 0.x worker's is.
    const before = await heldSlots(env.DB, env);
    await submit({ substrate: "do_v1" });
    const after = await heldSlots(env.DB, env);
    expect(after.orchestrators).toBe(before.orchestrators + 1);
  });
});
