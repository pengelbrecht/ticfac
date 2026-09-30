/**
 * Giving the account's container slots back when a run is over (hn6's cloud
 * run run_8511bc66…, 2026-09-30): the run failed, and `wrangler containers
 * instances` still showed two of its worker containers `running` — nothing
 * destroyed a run's workers when it ended, so they held two of the account's
 * three slots after it.
 *
 * Driven through the real D1 (the boot, settlement and reclaim records) and a
 * fake `SANDBOXES` binding, the one substitution every suite here makes. The
 * reclaim's three entry points are each exercised: finalize (every ending of
 * a run), the hourly `scheduled` sweep, and the function both call.
 */
import { createExecutionContext, env, waitOnExecutionContext } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { heldSlots, listReclaims, reclaimRunWorkers } from "../src/container-capacity";
import { insertRun, type Run, recordSandboxAttemptBoot, recordSandboxJobSettled } from "../src/db";
import worker, { type Env } from "../src/index";
import { finalize } from "../src/run-workflow";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../src/sandbox";
import { attemptSandboxName } from "../src/sandbox-executor";
import { WORKER_CANCEL_COMMAND, WORKER_COMMAND } from "../src/worker-boot";

// ------------------------------------------------------------ the fakes ---

class FakeProcess {
  state: SandboxProcessState = "running";
  exit_code: number | null = null;
  constructor(
    readonly id: string,
    readonly command: string,
  ) {}
  get view(): SandboxProcessView {
    return { id: this.id, state: this.state, exit_code: this.exit_code, command: this.command };
  }
}

/**
 * A worker container. Its work process is running until the stop-and-push
 * door is knocked on (`ticks-worker --cancel <reason>`), at which point the
 * worker's own salvage runs and it exits — what image/worker.sh does.
 */
class FakeWorker implements OrchestratorSandbox {
  readonly processes: FakeProcess[] = [];
  readonly asked: string[] = [];
  destroyed = false;
  listed = 0;
  #next = 0;
  constructor(readonly name: string) {}

  /** Puts a live work process in the container, as a dispatch leaves it. */
  working(): FakeProcess {
    const work = new FakeProcess(`${this.name}-w${++this.#next}`, WORKER_COMMAND);
    this.processes.push(work);
    return work;
  }

  async startProcess(command: string): Promise<SandboxProcessView> {
    const process = new FakeProcess(`${this.name}-p${++this.#next}`, command);
    if (command.startsWith(WORKER_CANCEL_COMMAND)) {
      this.asked.push(command);
      for (const work of this.processes.filter((p) => p.command === WORKER_COMMAND)) {
        work.state = "completed";
        work.exit_code = 0;
      }
      process.state = "completed";
      process.exit_code = 0;
    }
    this.processes.push(process);
    return process.view;
  }
  async getProcess(id: string): Promise<SandboxProcessView | null> {
    return this.processes.find((p) => p.id === id)?.view ?? null;
  }
  async listProcesses(): Promise<SandboxProcessView[]> {
    // The real SDK starts a stopped container on any call; a destroyed one
    // asked again would be that cold boot.
    if (this.destroyed) throw new Error(`listProcesses cold-booted destroyed ${this.name}`);
    this.listed += 1;
    return this.processes.map((p) => p.view);
  }
  async readOutput(_id: string, offset: number): Promise<SandboxOutput> {
    return { text: "", offset };
  }
  async killProcess(): Promise<void> {}
  async destroy(): Promise<void> {
    this.destroyed = true;
  }
}

class FakeWorkers implements SandboxBinding {
  readonly byName = new Map<string, FakeWorker>();
  async get(name: string): Promise<OrchestratorSandbox> {
    return this.named(name);
  }
  named(name: string): FakeWorker {
    let sandbox = this.byName.get(name);
    if (sandbox === undefined) {
      sandbox = new FakeWorker(name);
      this.byName.set(name, sandbox);
    }
    return sandbox;
  }
}

// ---------------------------------------------------------- the harness ---

const BASE_SHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2";
let counter = 0;
let binding: FakeWorkers;
const saved: Record<string, unknown> = {};

function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}

beforeEach(() => {
  binding = new FakeWorkers();
  set("SANDBOXES", binding);
});

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

async function aRun(state: Run["state"]): Promise<Run> {
  counter += 1;
  const run: Run = {
    run_id: `run_reclaim_${counter}`,
    project: `example-org/reclaim-${counter}`,
    epic: "hn6",
    base_sha: BASE_SHA,
    requested_by: "operator",
    state,
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  };
  await insertRun(env.DB, run);
  return run;
}

/**
 * The run's worker containers as the door leaves them: `3gk` working, `7uv`
 * working, and `ltg` settled (its container reclaimed at settlement).
 */
async function workersOf(run: Run): Promise<{ live: FakeWorker[]; settled: FakeWorker }> {
  const at = new Date().toISOString();
  const live: FakeWorker[] = [];
  for (const tick of ["3gk", "7uv"]) {
    await recordSandboxAttemptBoot(env.DB, {
      run_id: run.run_id,
      tick_id: tick,
      attempt: 1,
      job: "",
      model: "workers-ai/@cf/zai-org/glm-5.3",
      at,
    });
    const container = binding.named(attemptSandboxName(run.run_id, tick, 1));
    container.working();
    live.push(container);
  }
  await recordSandboxAttemptBoot(env.DB, {
    run_id: run.run_id,
    tick_id: "ltg",
    attempt: 2,
    job: "",
    model: "workers-ai/@cf/zai-org/glm-5.3",
    at,
  });
  await recordSandboxJobSettled(env.DB, {
    run_id: run.run_id,
    tick_id: "ltg",
    attempt: 2,
    job: "",
    state: "completed",
    exit_code: 0,
    at,
  });
  return { live, settled: binding.named(attemptSandboxName(run.run_id, "ltg", 2)) };
}

const instant = async (): Promise<void> => {};

// ---------------------------------------------------------------- tests ---

describe("reclaiming a run's worker containers", () => {
  it("asks each live worker to stop and push, then destroys every container and records each", async () => {
    const run = await aRun("failed");
    const { live, settled } = await workersOf(run);
    const before = await heldSlots(env.DB, env);

    const reclaimed = await reclaimRunWorkers(env.DB, binding, run.run_id, {
      reason: "run_ended:failed",
      sleep: instant,
    });

    expect(reclaimed.map((r) => r.tick_id).sort()).toEqual(["3gk", "7uv", "ltg"]);
    for (const container of live) {
      // The push first: the worker's own stop-and-push door, with the reason.
      expect(container.asked).toEqual([`${WORKER_CANCEL_COMMAND} run_ended:failed`]);
      expect(container.destroyed).toBe(true);
    }
    // A settled job's container is destroyed as the backstop, never asked —
    // asking a stopped container would cold-boot it.
    expect(settled.listed).toBe(0);
    expect(settled.asked).toEqual([]);
    expect(settled.destroyed).toBe(true);

    const records = await listReclaims(env.DB, run.run_id);
    expect(records).toHaveLength(3);
    expect(records.filter((r) => r.salvaged).map((r) => r.tick_id)).toEqual(["3gk", "7uv"]);
    for (const record of records) expect(record.reason).toBe("run_ended:failed");

    // The two live workers were holding slots; they are given back.
    const after = await heldSlots(env.DB, env);
    expect(after.workers).toBe(before.workers - 2);

    // A second reclaim (a retried finalize) finds nothing left to do.
    expect(
      await reclaimRunWorkers(env.DB, binding, run.run_id, { reason: "x", sleep: instant }),
    ).toEqual([]);
  });

  it("finalize reclaims the run's workers on every ending — here a failure", async () => {
    const run = await aRun("running");
    const { live } = await workersOf(run);

    await finalize(
      env as unknown as Env,
      {
        run_id: run.run_id,
        project: run.project,
        epic: run.epic,
        base_sha: BASE_SHA,
        requested_by: "operator",
        lease_token: "no-lease",
      },
      { state: "failed", detail: "the orchestrator exited 7 (boot 3)", boots: 0 },
      0,
    );

    for (const container of live) {
      expect(container.asked).toEqual([`${WORKER_CANCEL_COMMAND} run_ended:failed`]);
      expect(container.destroyed).toBe(true);
    }
    expect((await listReclaims(env.DB, run.run_id)).map((r) => r.tick_id).sort()).toEqual([
      "3gk",
      "7uv",
      "ltg",
    ]);
  });

  it("the hourly sweep reclaims the containers of a run that is over, and leaves a live run's alone", async () => {
    const over = await aRun("failed");
    const dead = await workersOf(over);
    const alive = await aRun("running");
    const working = await workersOf(alive);

    const ctx = createExecutionContext();
    const controller = {
      scheduledTime: Date.now(),
      cron: "0 * * * *",
      noRetry() {},
    } as ScheduledController;
    await worker.scheduled!(controller, env as unknown as Env, ctx);
    await waitOnExecutionContext(ctx);

    for (const container of dead.live) {
      expect(container.asked).toEqual([`${WORKER_CANCEL_COMMAND} run_not_live`]);
      expect(container.destroyed).toBe(true);
    }
    const records = await listReclaims(env.DB, over.run_id);
    expect(records.map((r) => r.tick_id).sort()).toEqual(["3gk", "7uv", "ltg"]);
    for (const record of records) expect(record.reason).toBe("run_not_live");

    // The live run's workers are its own: never asked, never destroyed.
    for (const container of working.live) {
      expect(container.asked).toEqual([]);
      expect(container.destroyed).toBe(false);
    }
    expect(await listReclaims(env.DB, alive.run_id)).toEqual([]);
  });
});
