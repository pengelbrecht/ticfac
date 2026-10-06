/**
 * The factory finishes run records whose Workflow instance is gone (tick
 * gbg). Evidence: run_9e40b956, run_2e66e765 and run_f15efdfb sat at
 * `stopping` for six weeks with no instance on Cloudflare, counted as live by
 * every reader — and held the 0.x container application in place after the
 * umq cutover, because the deploy will not delete an application a "live" run
 * is on.
 *
 * Driven through the real D1 and the real RunRoom; the Workflows binding is
 * the one fake, because what is under test is how its three answers — an
 * instance, no instance, a failed read — are told apart.
 */
import { env } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";
import { getRun, getRunProgress, insertRun, listDispatchLogs, type Run } from "../src/db";
import { issueRunToken } from "../src/gateway";
import {
  isInstanceNotFound,
  ORPHAN_MIN_AGE_MS,
  readRunInstance,
  sweepOrphanedRuns,
} from "../src/run-orphans";
import { type RunWorkflowBinding, roomFor, stopRun } from "../src/runs";

const BASE_SHA = "c".repeat(40);
const HOUR_AGO = () => new Date(Date.now() - 2 * ORPHAN_MIN_AGE_MS).toISOString();
let counter = 0;

/** Each instance as the fake Workflows binding answers for it. */
type Answer = { status: string } | "absent" | "absent-prod" | "unreadable";

function fakeWorkflows(answers: Record<string, Answer>): RunWorkflowBinding {
  return {
    async create() {
      throw new Error("the sweep never creates an instance");
    },
    async get(id: string) {
      const answer = answers[id];
      // The two shapes the platform raises a missing instance in: the local
      // runtime's, and production's (read off the live factory's log).
      if (answer === undefined || answer === "absent") throw new Error("instance.not_found");
      if (answer === "absent-prod") throw new Error("(instance.not_found) Instance not found");
      if (answer === "unreadable") throw new Error("internal error; reference = abc123");
      return {
        id,
        async status() {
          return answer;
        },
      };
    },
  };
}

const saved: Record<string, unknown> = {};
function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}
afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

async function aRun(state: Run["state"], startedAt: string = HOUR_AGO()): Promise<Run> {
  counter += 1;
  const run: Run = {
    run_id: `run_orphan_${counter}_${crypto.randomUUID().replaceAll("-", "").slice(0, 8)}`,
    project: `example-org/orphans-${counter}`,
    epic: "gbg",
    base_sha: BASE_SHA,
    requested_by: "operator",
    state,
    started_at: startedAt,
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  };
  await insertRun(env.DB, run);
  return run;
}

async function liveTokenCount(runID: string): Promise<number> {
  const row = await env.DB.prepare(
    "SELECT COUNT(*) AS n FROM run_gateway_token WHERE run_id = ? AND revoked_at IS NULL",
  )
    .bind(runID)
    .first<{ n: number }>();
  return row?.n ?? 0;
}

/** Sweeps with only `answers` on the binding; every OTHER live row reads as running. */
async function sweep(answers: Record<string, Answer>) {
  const others = await env.DB.prepare(
    "SELECT run_id FROM runs WHERE state IN ('starting','running','stopping')",
  ).all<{ run_id: string }>();
  const all: Record<string, Answer> = {};
  for (const { run_id } of others.results) all[run_id] = { status: "running" };
  set("RUN_WORKFLOW", fakeWorkflows({ ...all, ...answers }));
  return await sweepOrphanedRuns(env as never);
}

describe("the instance read keeps its three answers apart", () => {
  it("reads the platform's not-found, in both shapes, as absent", async () => {
    const binding = fakeWorkflows({ a: "absent", b: "absent-prod", c: "unreadable" });
    expect((await readRunInstance(binding, "a")).kind).toBe("absent");
    expect((await readRunInstance(binding, "b")).kind).toBe("absent");
    expect((await readRunInstance(binding, "c")).kind).toBe("unreadable");
    expect(isInstanceNotFound(new Error("timeout"))).toBe(false);
    expect(isInstanceNotFound("instance.not_found")).toBe(false);
  });
});

describe("the hourly sweep finishes orphaned run records", () => {
  it("finishes a stopping record with no Workflow instance as stopped, and releases its lease", async () => {
    const run = await aRun("stopping");
    const room = roomFor(env as never, run.project);
    const lease = await room.acquireDispatchLease({ run_id: run.run_id, epic: run.epic });
    expect(lease.ok).toBe(true);
    await issueRunToken(env as never, { run_id: run.run_id, tick_id: "abc", attempt: 1 });
    expect(await liveTokenCount(run.run_id)).toBe(1);

    const outcomes = await sweep({ [run.run_id]: "absent-prod" });

    const mine = outcomes.find((o) => o.run_id === run.run_id);
    expect(mine).toMatchObject({ outcome: "finished", state: "stopped", supervisor: "absent" });
    const stored = await getRun(env.DB, run.run_id);
    expect(stored?.state).toBe("stopped");
    expect(stored?.ended_at).not.toBeNull();
    // The reason is recorded where status reads a finished run's verdict.
    const progress = await getRunProgress(env.DB, run.run_id);
    expect(progress?.detail).toContain("orphaned");
    expect(progress?.detail).toContain("no Workflow instance");
    expect(await room.leaseStatus()).toBeNull();
    expect(await liveTokenCount(run.run_id)).toBe(0);
    const log = await listDispatchLogs(env.DB, run.run_id, run.epic);
    expect(log.map((l) => l.decision)).toContain("finished:stopped");
  });

  it("finishes a running record whose instance errored as failed", async () => {
    const run = await aRun("running");
    const outcomes = await sweep({ [run.run_id]: { status: "errored" } });
    expect(outcomes.find((o) => o.run_id === run.run_id)).toMatchObject({
      outcome: "finished",
      state: "failed",
      supervisor: "errored",
    });
    expect((await getRun(env.DB, run.run_id))?.state).toBe("failed");
  });

  it("leaves a record whose instance read fails transiently, and its lease", async () => {
    const run = await aRun("stopping");
    const room = roomFor(env as never, run.project);
    await room.acquireDispatchLease({ run_id: run.run_id, epic: run.epic });

    const outcomes = await sweep({ [run.run_id]: "unreadable" });

    expect(outcomes.find((o) => o.run_id === run.run_id)).toMatchObject({
      outcome: "left",
      undecided: true,
    });
    expect((await getRun(env.DB, run.run_id))?.state).toBe("stopping");
    expect((await room.leaseStatus())?.run_id).toBe(run.run_id);
  });

  it("leaves a record whose instance is still running", async () => {
    const run = await aRun("running");
    const outcomes = await sweep({ [run.run_id]: { status: "running" } });
    expect(outcomes.find((o) => o.run_id === run.run_id)).toMatchObject({
      outcome: "left",
      undecided: false,
    });
    expect((await getRun(env.DB, run.run_id))?.state).toBe("running");
  });

  it("does not ask about a record too young to have its instance yet", async () => {
    const run = await aRun("starting", new Date().toISOString());
    const outcomes = await sweep({ [run.run_id]: "absent" });
    expect(outcomes.find((o) => o.run_id === run.run_id)).toBeUndefined();
    expect((await getRun(env.DB, run.run_id))?.state).toBe("starting");
  });

  it("never releases a lease another run holds", async () => {
    const run = await aRun("stopping");
    const room = roomFor(env as never, run.project);
    await room.acquireDispatchLease({ run_id: "run_someone_else", epic: "xyz" });

    const outcomes = await sweep({ [run.run_id]: "absent" });

    expect(outcomes.find((o) => o.run_id === run.run_id)).toMatchObject({ outcome: "finished" });
    expect((await room.leaseStatus())?.run_id).toBe("run_someone_else");
  });
});

describe("a stop on a record whose instance is gone", () => {
  it("finishes the stop instead of freezing the record at stopping", async () => {
    const run = await aRun("running");
    set("RUN_WORKFLOW", fakeWorkflows({ [run.run_id]: "absent" }));

    const result = await stopRun(env as never, run.run_id, "operator");

    expect(result).toMatchObject({ outcome: "stopping", supervisor_ended: "absent" });
    expect((await getRun(env.DB, run.run_id))?.state).toBe("stopped");
  });

  it("leaves the stop to the supervisor when its instance cannot be read", async () => {
    const run = await aRun("running");
    set("RUN_WORKFLOW", fakeWorkflows({ [run.run_id]: "unreadable" }));

    await stopRun(env as never, run.run_id, "operator");

    expect((await getRun(env.DB, run.run_id))?.state).toBe("stopping");
  });
});
