import { env, SELF } from "cloudflare:test";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { deriveTokenHash, FEED_RELAY_PATH, mintFactoryToken } from "../src/auth";
import { enrolProject, insertRun, type Run } from "../src/db";
import {
  appendBootFeed,
  bootFeedKey,
  orchestratorBootFeedEvent,
  orchestratorExitFeedEvent,
  relayFeedKey,
} from "../src/feed-relay";
import { issueRunToken, issueWorkerRunToken } from "../src/gateway";
import {
  appendFeed,
  FINAL_FEED_SEQ,
  feedSegmentKey,
  runFinishedFeedEvent,
  runStartedFeedEvent,
  START_FEED_SEQ,
} from "../src/run-feed";

/**
 * The feed relay (src/feed-relay.ts): the orchestrator container relays the
 * reconciler's own event feed into the factory's run feed, so `ticfac
 * events/status/watch` show a cloud run exactly like a local one.
 *
 * The first cloud run of epic hn6 ran 1h45m — dispatched, gated, repaired
 * three times, rebooted three times — and its factory feed held exactly TWO
 * lines, because the reconciler's feed was written inside the container and
 * nothing carried it out. These tests pin the door that carries it: append
 * and read back through the route the CLI follows, idempotent replays, no
 * holes, ordering a byte-cursor follower can trust, and who may knock.
 *
 * And the second defect of that night: the container's parked-question sweep
 * was refused the pending list with 401 on every boot, because the route took
 * only the operator's token and a container holds only its run's.
 */

const BASE = "https://factory.example.com";
const PROJECT = "example-org/feed-relay";

let operatorToken: string;
const originalHash = env.FACTORY_TOKEN_HASH;

beforeAll(async () => {
  operatorToken = mintFactoryToken();
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(operatorToken);
  await enrolProject(env.DB, {
    project: PROJECT,
    enrolled_by: "operator@example.com",
    enrolled_at: new Date().toISOString(),
  });
});

afterAll(() => {
  if (originalHash === undefined) delete env.FACTORY_TOKEN_HASH;
  else env.FACTORY_TOKEN_HASH = originalHash;
});

let counter = 0;

/** A live run with its orchestrator's boot-N credential. */
async function liveRun(boot = 1): Promise<{ run: Run; token: string }> {
  const run: Run = {
    run_id: `run_relay_${++counter}`,
    project: PROJECT,
    epic: "hn6",
    base_sha: "b".repeat(40),
    requested_by: "operator",
    state: "running",
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  };
  await insertRun(env.DB, run);
  const { token } = await issueRunToken(env, { run_id: run.run_id, tick_id: "hn6", attempt: boot });
  return { run, token };
}

/** One reconciler feed line, exactly as internal/runfeed writes it. */
function line(runID: string, stage: string, detail: string, tick: string | null = null): string {
  return `${JSON.stringify({
    schema_version: 1,
    at: "2026-09-29T01:02:03.456789Z",
    run_id: runID,
    tick_id: tick,
    attempt: tick === null ? null : 1,
    stage,
    detail,
  })}\n`;
}

function relay(token: string, body: unknown): Promise<Response> {
  return SELF.fetch(`${BASE}${FEED_RELAY_PATH}`, {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

/** The feed as `ticfac events` reads it: the operator's route, not R2 directly. */
async function feedText(runID: string): Promise<string> {
  const res = await SELF.fetch(`${BASE}/api/runs/${runID}/events`, {
    headers: { authorization: `Bearer ${operatorToken}` },
  });
  expect(res.status).toBe(200);
  const body = (await res.json()) as { text: string; total_bytes: number };
  expect(body.total_bytes).toBe(new TextEncoder().encode(body.text).length);
  return body.text;
}

function stages(text: string): string[] {
  return text
    .split("\n")
    .filter((one) => one !== "")
    .map((one) => (JSON.parse(one) as { stage: string }).stage);
}

describe("the reconciler's feed, relayed into the factory's run feed", () => {
  it("appends relayed batches and serves them through /api/runs/:id/events in order", async () => {
    const { run, token } = await liveRun();
    await appendFeed(env, {
      project: PROJECT,
      run_id: run.run_id,
      seq: START_FEED_SEQ,
      events: [runStartedFeedEvent({ run_id: run.run_id, detail: "run started" })],
    });

    const first = line(run.run_id, "claimed", "claimed r5i", "r5i");
    const second =
      line(run.run_id, "dispatched", "attempt 1 started", "r5i") +
      line(run.run_id, "gate_started", "gate on r5i — go test", "r5i");

    const a = await relay(token, { stream: "feed", offset: 0, text: first });
    expect(a.status).toBe(201);
    expect(await a.json()).toMatchObject({ stored: true, boot: 1, lines: 1 });
    const b = await relay(token, {
      stream: "feed",
      offset: new TextEncoder().encode(first).length,
      text: second,
    });
    expect(b.status).toBe(201);

    const text = await feedText(run.run_id);
    expect(stages(text)).toEqual(["dispatched", "claimed", "dispatched", "gate_started"]);
    // Every line is the closed contract shape, with the run's own identity.
    for (const one of text.split("\n").filter((l) => l !== "")) {
      expect(JSON.parse(one).run_id).toBe(run.run_id);
    }
  });

  it("answers a replayed batch as stored and never writes it twice", async () => {
    const { run, token } = await liveRun();
    const text = line(run.run_id, "claimed", "claimed r5i", "r5i");
    expect((await relay(token, { stream: "feed", offset: 0, text })).status).toBe(201);
    const again = await relay(token, { stream: "feed", offset: 0, text });
    expect(again.status).toBe(200);
    expect(await again.json()).toMatchObject({ stored: false });
    expect(stages(await feedText(run.run_id))).toEqual(["claimed"]);
  });

  it("refuses a gap or an overlap with the offset the stream stands at", async () => {
    const { run, token } = await liveRun();
    const text = line(run.run_id, "claimed", "claimed r5i", "r5i");
    const size = new TextEncoder().encode(text).length;
    expect((await relay(token, { stream: "feed", offset: 0, text })).status).toBe(201);

    const gap = await relay(token, { stream: "feed", offset: size + 10, text });
    expect(gap.status).toBe(409);
    expect(await gap.json()).toMatchObject({ error: "offset_mismatch", expected: size });

    const overlap = await relay(token, { stream: "feed", offset: 5, text });
    expect(overlap.status).toBe(409);
    expect(stages(await feedText(run.run_id))).toEqual(["claimed"]);
  });

  it("tells a restarted relay where its stream stands", async () => {
    const { run, token } = await liveRun();
    const text = line(run.run_id, "claimed", "claimed r5i", "r5i");
    await relay(token, { stream: "feed", offset: 0, text });
    const res = await SELF.fetch(`${BASE}${FEED_RELAY_PATH}?stream=feed`, {
      headers: { authorization: `Bearer ${token}` },
    });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({
      run_id: run.run_id,
      boot: 1,
      stream: "feed",
      end: new TextEncoder().encode(text).length,
    });
  });

  it("renames the reconciler's terminal lines — a boot ending is not the run ending", async () => {
    const { run, token } = await liveRun();
    const text =
      line(run.run_id, "run_finished", "failed: gate red") + line(run.run_id, "run_died", "x");
    expect((await relay(token, { stream: "feed", offset: 0, text })).status).toBe(201);
    expect(stages(await feedText(run.run_id))).toEqual([
      "orchestrator_finished",
      "orchestrator_died",
    ]);
  });

  it("orders each boot: Workflow start, lifecycle, reconciler feed, Workflow exit — then the next boot", async () => {
    const boot1 = await liveRun(1);
    const runID = boot1.run.run_id;
    await appendBootFeed(env, {
      project: PROJECT,
      run_id: runID,
      boot: 1,
      slot: "a",
      event: orchestratorBootFeedEvent(runID, "orchestrator boot 1 starting"),
    });
    await relay(boot1.token, {
      stream: "feed",
      offset: 0,
      text: line(runID, "claimed", "claimed", "r5i"),
    });
    await relay(boot1.token, {
      stream: "boot",
      offset: 0,
      text: line(runID, "container", "probes green"),
    });
    await appendBootFeed(env, {
      project: PROJECT,
      run_id: runID,
      boot: 1,
      slot: "d",
      event: orchestratorExitFeedEvent(runID, "the orchestrator exited 1 (boot 1)"),
    });
    // Boot 2's credential rotates the run's token and carries the boot number.
    const { token: boot2 } = await issueRunToken(env, {
      run_id: runID,
      tick_id: "hn6",
      attempt: 2,
    });
    await relay(boot2, { stream: "feed", offset: 0, text: line(runID, "resumed", "boot 2") });
    await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq: FINAL_FEED_SEQ,
      events: [runFinishedFeedEvent({ run_id: runID, detail: "failed: boots exhausted" })],
    });

    expect(stages(await feedText(runID))).toEqual([
      "orchestrator_boot",
      "container",
      "claimed",
      "orchestrator_exit",
      "resumed",
      "run_finished",
    ]);
    expect(bootFeedKey(PROJECT, runID, 1, "a") < relayFeedKey(PROJECT, runID, 1, "boot", 0)).toBe(
      true,
    );
    expect(relayFeedKey(PROJECT, runID, 9, "feed", 999) < feedSegmentKey(PROJECT, runID, 2)).toBe(
      true,
    );
  });

  it("closes a boot's slice once its exit line stands, so a follower's cursor never moves", async () => {
    const { run, token } = await liveRun();
    await appendBootFeed(env, {
      project: PROJECT,
      run_id: run.run_id,
      boot: 1,
      slot: "d",
      event: orchestratorExitFeedEvent(run.run_id, "the orchestrator exited 0 (boot 1)"),
    });
    const late = await relay(token, {
      stream: "feed",
      offset: 0,
      text: line(run.run_id, "claimed", "late"),
    });
    expect(late.status).toBe(409);
    expect(await late.json()).toMatchObject({ error: "feed_closed" });
  });

  it("refuses a line the strict follower would refuse, and a line naming another run", async () => {
    const { run, token } = await liveRun();
    const extra = `${JSON.stringify({ ...JSON.parse(line(run.run_id, "x", "y")), cost_usd: 1 })}\n`;
    expect((await relay(token, { stream: "feed", offset: 0, text: extra })).status).toBe(400);
    const foreign = line("run_someone_else", "claimed", "x");
    expect((await relay(token, { stream: "feed", offset: 0, text: foreign })).status).toBe(400);
    const partial = line(run.run_id, "claimed", "x").trimEnd();
    expect((await relay(token, { stream: "feed", offset: 0, text: partial })).status).toBe(400);
  });

  it("takes only the run's orchestrator credential — never a worker's, never the operator's", async () => {
    const { run } = await liveRun();
    const { token: worker } = await issueWorkerRunToken(env, {
      run_id: run.run_id,
      tick_id: "r5i",
      attempt: 1,
    });
    const text = line(run.run_id, "claimed", "x");
    const byWorker = await relay(worker, { stream: "feed", offset: 0, text });
    expect(byWorker.status).toBe(403);
    expect(await byWorker.json()).toMatchObject({ error: "not_orchestrator" });
    expect((await relay(operatorToken, { stream: "feed", offset: 0, text })).status).toBe(401);
  });
});

describe("the container's parked-question sweep, on its run's credential", () => {
  const pending = (project: string) => `${BASE}/api/projects/${project}/pending`;

  it("lists its own project's pending questions (was 401 unauthorized)", async () => {
    const { token } = await liveRun();
    const res = await SELF.fetch(`${pending(PROJECT)}?include_resolved=true`, {
      headers: { authorization: `Bearer ${token}` },
    });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ pending: [] });
  });

  it("registers a question for its own project", async () => {
    const { token } = await liveRun();
    const res = await SELF.fetch(pending(PROJECT), {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify({
        id: `q-relay-${counter}`,
        tick_id: "r5i",
        epic: "hn6",
        kind: "gate",
        question: { header: "Merge", text: "Ship it?", options: [{ id: "ok", label: "OK" }] },
      }),
    });
    expect(res.status).toBe(201);
  });

  it("is refused another project's questions, and every answering route", async () => {
    const { token } = await liveRun();
    const other = await SELF.fetch(pending("example-org/someone-else"), {
      headers: { authorization: `Bearer ${token}` },
    });
    expect(other.status).toBe(403);
    expect(await other.json()).toMatchObject({ error: "wrong_project" });

    const answer = await SELF.fetch(`${pending(PROJECT)}/q-any/answer`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify({ outcome: { option_ids: ["ok"] } }),
    });
    expect(answer.status).toBe(401);
  });

  it("is refused once the run's credential is revoked or belongs to a worker", async () => {
    const { run } = await liveRun();
    const { token: worker } = await issueWorkerRunToken(env, {
      run_id: run.run_id,
      tick_id: "r5i",
      attempt: 1,
    });
    const res = await SELF.fetch(pending(PROJECT), {
      headers: { authorization: `Bearer ${worker}` },
    });
    expect(res.status).toBe(403);
  });
});

describe("a run asking whether a claim's holder is still running (hn6 cloud-run stall)", () => {
  const status = (runID: string) => `${BASE}/api/runs/${runID}`;

  // The holder's Workflow instance, as the factory reads it: ended. A stand-in
  // for the binding, so the read answers what a dead run's instance says.
  const originalWorkflow = env.RUN_WORKFLOW;
  beforeEach(() => {
    env.RUN_WORKFLOW = {
      async get(id: string) {
        return {
          id,
          async status() {
            return { status: "complete" };
          },
        };
      },
    } as unknown as typeof env.RUN_WORKFLOW;
  });
  afterEach(() => {
    env.RUN_WORKFLOW = originalWorkflow;
  });

  /** A run of the same epic that ended: the claim holder a new run asks about. */
  async function endedRun(project = PROJECT): Promise<Run> {
    const run: Run = {
      run_id: `run_ended_${++counter}`,
      project,
      epic: "hn6",
      base_sha: "b".repeat(40),
      requested_by: "operator",
      state: "failed",
      started_at: new Date().toISOString(),
      ended_at: new Date().toISOString(),
      cost_usd: 0,
      trace_id: null,
      credential_grade: "write",
    };
    await insertRun(env.DB, run);
    return run;
  }

  it("reads a run of its own project on its orchestrator credential (was 401 unauthorized)", async () => {
    const { token } = await liveRun();
    const holder = await endedRun();
    const res = await SELF.fetch(status(holder.run_id), {
      headers: { authorization: `Bearer ${token}` },
    });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({
      run: { run_id: holder.run_id, state: "failed" },
      phase: { workflow: { id: holder.run_id, status: "complete" } },
    });
  });

  it("is refused another project's run, and an unknown one, without naming either", async () => {
    const { token } = await liveRun();
    const foreign = await endedRun("example-org/someone-else");
    const other = await SELF.fetch(status(foreign.run_id), {
      headers: { authorization: `Bearer ${token}` },
    });
    expect(other.status).toBe(403);
    const body = (await other.json()) as { error: string; detail: string };
    expect(body.error).toBe("wrong_project");
    expect(body.detail).not.toContain("someone-else");

    const unknown = await SELF.fetch(status("run_nobody"), {
      headers: { authorization: `Bearer ${token}` },
    });
    expect(unknown.status).toBe(404);
  });

  it("is refused a worker's credential, and every write below the run", async () => {
    const { run } = await liveRun();
    const holder = await endedRun();
    const { token: worker } = await issueWorkerRunToken(env, {
      run_id: run.run_id,
      tick_id: "r5i",
      attempt: 1,
    });
    const byWorker = await SELF.fetch(status(holder.run_id), {
      headers: { authorization: `Bearer ${worker}` },
    });
    expect(byWorker.status).toBe(403);

    const { token } = await liveRun();
    const stop = await SELF.fetch(`${status(holder.run_id)}/stop`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify({ mode: "hard" }),
    });
    expect(stop.status).toBe(401);
  });
});
