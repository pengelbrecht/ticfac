import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import contract from "../../contracts/run-event-feed.json";

import {
  appendFeed,
  attemptsSoFar,
  type FeedEvent,
  FINAL_FEED_SEQ,
  feedSegmentKey,
  orchestratorUnanswerableFeedEvent,
  readRunFeed,
  readRunFeedPage,
  runFinishedFeedEvent,
  runStartedFeedEvent,
  START_FEED_SEQ,
  tickCollectedFeedEvent,
  tickDispatchedFeedEvent,
  UNANSWERABLE_FEED_SEQ,
  waveFeedSeq,
} from "../src/run-feed";
import { type Defs, parseDefs, parseSchema, type Schema, validate } from "./json-schema";

/**
 * The cloud run's half of the run event feed (tick k7p): the SAME line
 * schema the local reconciler writes, emitted by the other host, stored as
 * R2 segments a laptop reads through the factory's authenticated route.
 *
 * The tests here pin the three things the acceptance names:
 *
 * 1. ONE schema, both hosts — every builder's output is validated against
 *    this repository's own vendored `contracts/run-event-feed.json`, the same
 *    contract file `test/run-event-feed.test.ts` proves the goldens against.
 *    A cloud line that drifted from a local one is a dashboard that places
 *    one and refuses the other, which is the defect the "indistinguishable
 *    records" rule exists to prevent.
 *
 * 2. The segments are the appends — readable WHILE the run is going, ordered
 *    by key, and a replayed site rewrites its own key rather than interleaving
 *    a second copy of a line.
 *
 * 3. The sink never throws — a feed that could cost a run anything would be a
 *    gate, and the feed's first rule is that it never gates a run.
 */

const feedEvent = contract.records.feed_event as { schema_id: string; schema: unknown };
const defs: Defs = parseDefs({});
const schema: Schema = parseSchema(feedEvent.schema, "records.feed_event");

const PROJECT = "example-org/example-repo";

function assertContractLine(event: FeedEvent): void {
  const errors = validate(schema, defs, event as never);
  expect(errors, `${feedEvent.schema_id} must accept: ${JSON.stringify(event)}`).toEqual([]);
}

describe("the cloud run's feed lines", () => {
  it("validates every builder's line against the same contract as the local feed", () => {
    assertContractLine(
      runStartedFeedEvent({
        run_id: "run_62c289d1",
        detail: "run started: one orchestrator container",
      }),
    );
    assertContractLine(
      tickDispatchedFeedEvent({
        run_id: "run_62c289d1",
        tick_id: "a1",
        attempt: 1,
        detail: "worker container dispatched (wave 1, batch 1)",
      }),
    );
    assertContractLine(
      tickCollectedFeedEvent({
        run_id: "run_62c289d1",
        tick_id: "a1",
        attempt: 1,
        detail: "verdict ready-to-merge: STATUS: DONE",
      }),
    );
    assertContractLine(
      runFinishedFeedEvent({ run_id: "run_62c289d1", detail: "completed: every tick closed" }),
    );
    assertContractLine(
      orchestratorUnanswerableFeedEvent({
        run_id: "run_62c289d1",
        detail: "failed: the orchestrator's container could not be asked how it was doing",
      }),
    );
  });

  it("states tick_id and attempt as required-and-null, never omits them", () => {
    const started = runStartedFeedEvent({ run_id: "run_62c289d1", detail: "run started" });
    // A line that omits tick_id and one that states it as null are different
    // claims, and only the second is a claim at all — the contract's own rule.
    expect(started).toHaveProperty("tick_id", null);
    expect(started).toHaveProperty("attempt", null);
    expect(Object.keys(started).sort()).toEqual([
      "at",
      "attempt",
      "detail",
      "run_id",
      "schema_version",
      "stage",
      "tick_id",
    ]);
  });

  it("stamps `at` RFC3339 when the caller did not", () => {
    const event = tickDispatchedFeedEvent({
      run_id: "run_62c289d1",
      tick_id: "a1",
      attempt: 1,
      detail: "dispatched",
      at: "2026-09-14T12:00:00Z",
    });
    expect(event.at).toBe("2026-09-14T12:00:00Z");
    expect(runStartedFeedEvent({ run_id: "r", detail: "d" }).at).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/,
    );
  });

  it("counts attempts from what the run already collected, so a re-requested tick is attempt 2", () => {
    expect(attemptsSoFar([], "a1")).toBe(0);
    expect(attemptsSoFar(["a1", "b1"], "a1")).toBe(1);
    expect(attemptsSoFar(["a1", "a1"], "a1")).toBe(2);
    expect(attemptsSoFar(["a1"], "b1")).toBe(0);
  });
});

describe("the feed's segment keys", () => {
  it("allocates one slot per wave/batch site, with the collected segment after the started one", () => {
    const started = waveFeedSeq(0, 0, false)!;
    const collected = waveFeedSeq(0, 0, true)!;
    expect(started).toBeLessThan(collected);
    // Every later wave's every segment sorts after every earlier wave's:
    // the feed a reader concatenates in key order is the order the run
    // happened in.
    for (let wave = 1; wave < 12; wave += 1) {
      expect(waveFeedSeq(wave, 0, false)!).toBeGreaterThan(waveFeedSeq(wave - 1, 9998, true)!);
    }
  });

  it("places the run's start before every wave and its finish after all of them", () => {
    expect(START_FEED_SEQ).toBeLessThan(waveFeedSeq(0, 0, false)!);
    expect(FINAL_FEED_SEQ).toBeGreaterThan(waveFeedSeq(11, 9998, true)!);
  });

  it("places the unanswerable line after every wave and before the finish, one slot per run", () => {
    // A run-level line the watch writes mid-run (tick 3ed): above every wave
    // segment so it cannot interleave one, below the terminal line so it is
    // readable while the run is still unwinding, and one fixed key per run —
    // the work pass or the closeout pass can hold, never both, and a replayed
    // step rewrites the same key rather than appending a second copy.
    expect(UNANSWERABLE_FEED_SEQ).toBeGreaterThan(waveFeedSeq(86, 9998, true)!);
    expect(UNANSWERABLE_FEED_SEQ).toBeLessThan(FINAL_FEED_SEQ);
  });

  it("refuses a run past the spacing rather than interleaving its segments", () => {
    // A skipped append is one console line on a run nobody can watch live;
    // an interleaved one would be a feed that lies about order.
    expect(waveFeedSeq(87, 0, false)).toBeNull();
    expect(waveFeedSeq(0, 9999, false)).toBeNull();
    expect(waveFeedSeq(-1, 0, false)).toBeNull();
  });

  it("pads keys so lexicographic order is numeric order", () => {
    expect(feedSegmentKey(PROJECT, "run_x", START_FEED_SEQ).endsWith("0000000.jsonl")).toBe(true);
    expect(feedSegmentKey(PROJECT, "run_x", 12).endsWith("0000012.jsonl")).toBe(true);
  });
});

describe("the feed sink", () => {
  it("appends a segment and reads it back in key order, while the run is going", async () => {
    const runID = "run_feed_live";
    const wrote = await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq: START_FEED_SEQ,
      events: [
        runStartedFeedEvent({ run_id: runID, detail: "run started: 2 tick(s), 2 at a time" }),
      ],
    });
    expect(wrote).toBe(true);
    await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq: waveFeedSeq(0, 0, false)!,
      events: [
        tickDispatchedFeedEvent({ run_id: runID, tick_id: "a1", attempt: 1, detail: "dispatched" }),
        tickDispatchedFeedEvent({ run_id: runID, tick_id: "b1", attempt: 1, detail: "dispatched" }),
      ],
    });
    await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq: waveFeedSeq(0, 0, true)!,
      events: [
        tickCollectedFeedEvent({
          run_id: runID,
          tick_id: "a1",
          attempt: 1,
          detail: "verdict done: STATUS: DONE",
        }),
      ],
    });

    const text = await readRunFeed(env.ARTIFACTS!, PROJECT, runID);
    const lines = text
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line) as FeedEvent);
    expect(lines.map((line) => line.stage)).toEqual([
      "dispatched",
      "dispatched",
      "dispatched",
      "collected",
    ]);
    expect(lines[0]!.tick_id).toBeNull();
    expect(lines[1]!.tick_id).toBe("a1");
  });

  it("rewrites its own key on a replayed site, so no line is duplicated", async () => {
    const runID = "run_feed_replay";
    const seq = waveFeedSeq(0, 0, false)!;
    await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq,
      events: [
        tickDispatchedFeedEvent({ run_id: runID, tick_id: "a1", attempt: 1, detail: "dispatched" }),
      ],
    });
    // A checkpointed step that ran anyway (the same key, the same content):
    await appendFeed(env, {
      project: PROJECT,
      run_id: runID,
      seq,
      events: [
        tickDispatchedFeedEvent({ run_id: runID, tick_id: "a1", attempt: 1, detail: "dispatched" }),
      ],
    });
    const text = await readRunFeed(env.ARTIFACTS!, PROJECT, runID);
    expect(text.trim().split("\n")).toHaveLength(1);
  });

  it("never throws, whatever the bucket does", async () => {
    const broken = {
      put: () => {
        throw new Error("R2 is down");
      },
      list: () => {
        throw new Error("R2 is down");
      },
      get: () => {
        throw new Error("R2 is down");
      },
    } as unknown as R2Bucket;
    await expect(
      appendFeed({ ...env, ARTIFACTS: broken } as typeof env, {
        project: PROJECT,
        run_id: "run_feed_broken",
        seq: START_FEED_SEQ,
        events: [runStartedFeedEvent({ run_id: "run_feed_broken", detail: "started" })],
      }),
    ).resolves.toBe(false);
  });

  it("answers false for an out-of-spacing seq rather than guessing a key", async () => {
    await expect(
      appendFeed(env, {
        project: PROJECT,
        run_id: "run_feed_skip",
        seq: null,
        events: [runStartedFeedEvent({ run_id: "run_feed_skip", detail: "started" })],
      }),
    ).resolves.toBe(false);
  });
});

describe("a long run's feed, read a page at a time", () => {
  // run_5c7c16d1 (epic hn6, ~5h): the orchestrator's feed is relayed every
  // two seconds, so a long run leaves thousands of segments — and the reader
  // fetched EVERY one, one R2 GET after another, on every poll. The route
  // never answered inside the client's deadline, and `ticfac events` and
  // `ticfac status` failed on it every time. A page costs the same at hour
  // five as at minute one: the listing places the cursor, and only the
  // page's own segments are fetched.
  const SEGMENTS = 1200;

  async function writeLargeFeed(runID: string): Promise<string> {
    let whole = "";
    const puts: Promise<unknown>[] = [];
    for (let seq = 1; seq <= SEGMENTS; seq += 1) {
      const events = [
        tickDispatchedFeedEvent({
          run_id: runID,
          tick_id: `t${seq}`,
          attempt: 1,
          detail: `dispatched — segment ${seq}`,
          at: "2026-10-04T06:00:00Z",
        }),
        tickCollectedFeedEvent({
          run_id: runID,
          tick_id: `t${seq}`,
          attempt: 1,
          detail: `verdict done — segment ${seq}`,
          at: "2026-10-04T06:00:01Z",
        }),
      ];
      const text = `${events.map((event) => JSON.stringify(event)).join("\n")}\n`;
      whole += text;
      puts.push(env.ARTIFACTS!.put(feedSegmentKey(PROJECT, runID, seq), text));
    }
    await Promise.all(puts);
    return whole;
  }

  function counting(bucket: R2Bucket): { bucket: R2Bucket; gets: () => number } {
    let gets = 0;
    const wrapped = {
      list: (options: R2ListOptions) => bucket.list(options),
      get: (key: string) => {
        gets += 1;
        return bucket.get(key);
      },
    } as unknown as R2Bucket;
    return { bucket: wrapped, gets: () => gets };
  }

  it("serves a bounded page, fetching only the page's segments, and the pages walk back to the whole feed", async () => {
    const runID = "run_feed_large";
    const whole = await writeLargeFeed(runID);
    const total = new TextEncoder().encode(whole).length;

    const probe = counting(env.ARTIFACTS!);
    const first = await readRunFeedPage(probe.bucket, PROJECT, runID, { limit: 16 * 1024 });
    expect(first.from).toBe(0);
    expect(first.total_bytes).toBe(total);
    expect(first.more).toBe(true);
    expect(first.bytes).toBeLessThan(total);
    expect(first.bytes).toBeLessThanOrEqual(16 * 1024 + 1024);
    expect(probe.gets()).toBeLessThan(SEGMENTS / 10);
    expect(first.text.endsWith("\n")).toBe(true);

    // Walked by cursor, the pages ARE the feed: no line twice, no hole.
    let walked = "";
    let from = 0;
    let pages = 0;
    for (;;) {
      const page = await readRunFeedPage(env.ARTIFACTS!, PROJECT, runID, {
        from,
        limit: 64 * 1024,
      });
      expect(page.from).toBe(from);
      walked += page.text;
      pages += 1;
      if (!page.more) break;
      from = page.next;
    }
    expect(pages).toBeGreaterThan(1);
    expect(walked).toBe(whole);
    expect(await readRunFeed(env.ARTIFACTS!, PROJECT, runID)).toBe(whole);
  });

  it("resumes from a cursor inside a segment, at the line it names", async () => {
    const runID = "run_feed_mid";
    const whole = await writeLargeFeed(runID);
    const bytes = new TextEncoder().encode(whole);
    // The end of the first line: a cursor a follower holds after reading it.
    const cursor = bytes.indexOf(0x0a) + 1;
    const page = await readRunFeedPage(env.ARTIFACTS!, PROJECT, runID, {
      from: cursor,
      limit: 1024,
    });
    expect(page.from).toBe(cursor);
    expect(whole.startsWith(new TextDecoder().decode(bytes.subarray(0, cursor)) + page.text)).toBe(
      true,
    );
    expect((JSON.parse(page.text.split("\n")[0]!) as FeedEvent).stage).toBe("collected");
  });

  it("answers a tail from the feed's end, starting on a line boundary", async () => {
    const runID = "run_feed_tail";
    const whole = await writeLargeFeed(runID);
    const total = new TextEncoder().encode(whole).length;
    const probe = counting(env.ARTIFACTS!);
    const page = await readRunFeedPage(probe.bucket, PROJECT, runID, { tail: 2000 });
    expect(probe.gets()).toBeLessThan(20);
    expect(page.more).toBe(false);
    expect(page.next).toBe(total);
    expect(page.bytes).toBeGreaterThanOrEqual(2000);
    expect(whole.endsWith(page.text)).toBe(true);
    const lines = page.text.trim().split("\n");
    expect((JSON.parse(lines[0]!) as FeedEvent).stage).toBe("dispatched");
    expect((JSON.parse(lines[lines.length - 1]!) as FeedEvent).tick_id).toBe(`t${SEGMENTS}`);
  });

  it("answers an empty page for a feed with no segment", async () => {
    const page = await readRunFeedPage(env.ARTIFACTS!, PROJECT, "run_feed_none", {});
    expect(page).toEqual({ text: "", from: 0, bytes: 0, next: 0, total_bytes: 0, more: false });
  });
});
