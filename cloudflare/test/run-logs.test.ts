import { env, SELF } from "cloudflare:test";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  HARNESS_TAIL_MAX_BYTES,
  readHarnessTail,
  writeHarnessSegment,
  writeWorkerLogSegment,
} from "../src/artifacts";
import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { insertRun } from "../src/db";
import { appendFeed, FINAL_FEED_SEQ, feedSegmentKey, runFinishedFeedEvent } from "../src/run-feed";

/**
 * `tk cloud logs <run>` — what the container printed.
 *
 * The other half of "what happened" lives in AI Gateway and is read by
 * `tk cloud trace`; these two are deliberately separate commands, because a
 * harness that crashed and a model that decided badly are different failures
 * with different evidence.
 *
 * Both are read-only. Neither widens the operator-to-orchestrator command
 * vocabulary D21 fixes at run/stop/status/answer: they observe a run, and
 * nothing here can steer one.
 */

const BASE = "https://factory.example.com";
const PROJECT = "example-org/example-repo";
/** A well-formed trace id, of the shape ticfac/cloudflare/src/trace.ts mints. */
const TRACE_ID = "tr_0123456789abcdef0123456789abcdef";

let token: string;
const originalHash = env.FACTORY_TOKEN_HASH;

beforeAll(async () => {
  token = mintFactoryToken();
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
});

afterAll(() => {
  if (originalHash === undefined) delete env.FACTORY_TOKEN_HASH;
  else env.FACTORY_TOKEN_HASH = originalHash;
});

const get = (
  path: string,
  headers: Record<string, string> = { Authorization: `Bearer ${token}` },
) => SELF.fetch(`${BASE}${path}`, { headers });

async function recordedRun(runID: string): Promise<void> {
  await insertRun(env.DB, {
    run_id: runID,
    project: PROJECT,
    epic: "l4l",
    base_sha: "b".repeat(40),
    requested_by: "operator@example.com",
    state: "running",
    started_at: new Date(0).toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  });
}

describe("the harness tail reader", () => {
  it("returns the whole stream when it fits, in flush order", async () => {
    const runID = "run_tail_small";
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, runID, 1, 1, "first\n");
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, runID, 1, 2, "second\n");

    const output = await readHarnessTail(env.ARTIFACTS, PROJECT, runID);
    expect(output.text).toBe("first\nsecond\n");
    expect(output.truncated).toBe(false);
    expect(output.bytes).toBe(output.total_bytes);
  });

  it("keeps the END of an oversized stream and says it did", async () => {
    const runID = "run_tail_big";
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, runID, 1, 1, "oldest\n");
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, runID, 1, 2, "middle\n");
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, runID, 1, 3, "newest\n");

    // A budget that fits one flush: the tail is what a run being debugged is
    // read for, and the boundary falls between flushes rather than mid-line.
    const output = await readHarnessTail(env.ARTIFACTS, PROJECT, runID, 7);
    expect(output.text).toBe("newest\n");
    expect(output.truncated).toBe(true);
    expect(output.total_bytes).toBe(21);
    expect(output.bytes).toBe(7);
  });

  it("reports an empty stream as empty rather than truncated", async () => {
    const output = await readHarnessTail(env.ARTIFACTS, PROJECT, "run_tail_none");
    expect(output).toEqual({ text: "", bytes: 0, total_bytes: 0, truncated: false });
  });
});

describe("GET /api/runs/:id/logs", () => {
  it("serves the run's harness output with the run's own identity", async () => {
    const runID = "run_logs_ok";
    await recordedRun(runID);
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, runID, 1, 1, "booting orchestrator\n");

    const res = await get(`/api/runs/${runID}/logs`);
    expect(res.status).toBe(200);
    await expect(res.json()).resolves.toEqual({
      run_id: runID,
      project: PROJECT,
      state: "running",
      // Null here because `recordedRun` records a run with no chain; the
      // trace-id read is asserted on its own below.
      trace_id: null,
      text: "booting orchestrator\n",
      bytes: 21,
      total_bytes: 21,
      truncated: false,
      // Which worker containers left a stream of their own (tick 0fg). Empty
      // here: this run dispatched none.
      streams: [],
    });
  });

  // The trace id is served from the run's INDEX ROW, not scraped out of the
  // log text (tick hyi). The stream carries a banner of its own, but a read is
  // bounded from the END — so on the long-running container an operator most
  // wants the id for, the banner is the first thing to fall off the budget.
  // The row answers whatever the tail happens to contain, which is what makes
  // "recover the trace id from the worker's logs" one query rather than a
  // grep that sometimes works.
  it("states the run's trace id on every read, and null when the run has no chain", async () => {
    const traced = "run_logs_traced";
    await insertRun(env.DB, {
      run_id: traced,
      project: PROJECT,
      epic: "l4l",
      base_sha: "b".repeat(40),
      requested_by: "operator@example.com",
      state: "running",
      started_at: new Date(0).toISOString(),
      ended_at: null,
      cost_usd: 0,
      trace_id: TRACE_ID,
      credential_grade: "write",
    });
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, traced, 1, 1, "booting\n");
    await expect((await get(`/api/runs/${traced}/logs`)).json()).resolves.toMatchObject({
      trace_id: TRACE_ID,
    });
    // A per-tick read answers with the same id: the operator reading one
    // container's output is the one who needs it most.
    await writeWorkerLogSegment(env.ARTIFACTS, PROJECT, traced, "tap", 1, 1, "worker\n");
    await expect((await get(`/api/runs/${traced}/logs?tick=tap`)).json()).resolves.toMatchObject({
      trace_id: TRACE_ID,
      tick_id: "tap",
    });

    // Null, never a placeholder: a run started before trace ids existed
    // belongs to no chain, and "" would be a chain nobody can find.
    await recordedRun("run_logs_untraced");
    await writeHarnessSegment(env.ARTIFACTS, PROJECT, "run_logs_untraced", 1, 1, "booting\n");
    await expect((await get("/api/runs/run_logs_untraced/logs")).json()).resolves.toMatchObject({
      trace_id: null,
    });
  });

  it("is a 404 for a run this factory never recorded", async () => {
    const res = await get("/api/runs/run_missing/logs");
    expect(res.status).toBe(404);
  });

  it("requires the factory bearer token", async () => {
    const res = await SELF.fetch(`${BASE}/api/runs/run_logs_ok/logs`);
    expect(res.status).toBe(401);
  });

  it("is GET only — reading logs is not a way to command a run (D21)", async () => {
    const res = await SELF.fetch(`${BASE}/api/runs/run_logs_ok/logs`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.status).toBe(405);
  });

  it("refuses a max_bytes past the read bound rather than silently clamping", async () => {
    const runID = "run_logs_bound";
    await recordedRun(runID);
    const res = await get(`/api/runs/${runID}/logs?max_bytes=${HARNESS_TAIL_MAX_BYTES + 1}`);
    expect(res.status).toBe(400);
  });
});

describe("GET /api/runs/:id/events", () => {
  // The bare `ticfac` warned "the feed read for run_… carried 1132 bytes
  // against a 1130-byte standing size" for every cloud run: this route stated
  // `text.length` — UTF-16 code units — as the feed's byte size, and every
  // feed line carries an em dash (one code unit, three UTF-8 bytes). The size
  // is the cursor a follower walks the bytes by, so it is UTF-8 bytes.
  it("states the feed's size in UTF-8 bytes, not UTF-16 code units", async () => {
    const runID = "run_feed_bytes";
    await recordedRun(runID);
    await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq: FINAL_FEED_SEQ,
      events: [
        runFinishedFeedEvent({
          run_id: runID,
          detail: "failed: the orchestrator exited 8 — boot 3",
        }),
      ],
    });

    const res = await get(`/api/runs/${runID}/events`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { text: string; bytes: number; total_bytes: number };
    const utf8 = new TextEncoder().encode(body.text).length;
    expect(utf8).toBeGreaterThan(body.text.length);
    expect(body.bytes).toBe(utf8);
    expect(body.total_bytes).toBe(utf8);
  });

  // run_5c7c16d1 (epic hn6): the route read the whole feed on every request
  // and timed out on a long run. It serves PAGES now, addressed by byte
  // cursor, and says where the next one starts.
  it("serves a page from a cursor, a tail, and refuses a cursor it cannot read", async () => {
    const runID = "run_feed_paged_route";
    await recordedRun(runID);
    const lines: string[] = [];
    for (let seq = 1; seq <= 40; seq += 1) {
      const event = runFinishedFeedEvent({ run_id: runID, detail: `line ${seq} — of forty` });
      lines.push(`${JSON.stringify(event)}\n`);
      await env.ARTIFACTS!.put(feedSegmentKey(PROJECT, runID, seq), lines[lines.length - 1]!);
    }
    const whole = lines.join("");
    const total = new TextEncoder().encode(whole).length;

    type Page = {
      text: string;
      from: number;
      bytes: number;
      next: number;
      total_bytes: number;
      more: boolean;
    };
    let walked = "";
    let from = 0;
    for (let guard = 0; guard < 100; guard += 1) {
      const res = await get(`/api/runs/${runID}/events?from=${from}&limit=500`);
      expect(res.status).toBe(200);
      const page = (await res.json()) as Page;
      expect(page.from).toBe(from);
      expect(page.total_bytes).toBe(total);
      walked += page.text;
      if (!page.more) break;
      expect(page.bytes).toBeLessThan(total);
      from = page.next;
    }
    expect(walked).toBe(whole);

    const tail = (await (await get(`/api/runs/${runID}/events?tail=1`)).json()) as Page;
    expect(tail.text).toBe(lines[lines.length - 1]);
    expect(tail.next).toBe(total);

    expect((await get(`/api/runs/${runID}/events?from=-1`)).status).toBe(400);
    expect((await get(`/api/runs/${runID}/events?limit=0`)).status).toBe(400);
    expect((await get(`/api/runs/${runID}/events?from=0&tail=5`)).status).toBe(400);
  });
});
