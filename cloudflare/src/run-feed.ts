/**
 * The cloud run's half of the run event feed (tick k7p).
 *
 * `contracts/run-event-feed.json` pins ONE versioned line schema,
 * `ticfac.run_event.v1`, and until now only the local reconciler wrote it: a
 * cloud run published `run_event` board messages (run-events.ts) — a different
 * shape, a different vocabulary, readable only by a board that may not be
 * configured. An operator watching a Workflow-hosted run from a laptop had no
 * line a program could parse, which is the same position the local feed was
 * built to end (tick u9l).
 *
 * So this module is not a second feed. It is the SAME contract, emitted by the
 * OTHER host, with run/tick/attempt identity on every line — the
 * indistinguishable-records rule: a cloud run's lines validate against the
 * same schema a local run's do (test/run-event-feed.test.ts reads the same
 * contract file and runs these builders' output through it).
 *
 * Three rules carry over verbatim from run-events.ts and the contract:
 *
 * 1. **The feed is EXHAUST, never authority.** {@link appendFeed} cannot
 *    throw, has no retry, and returns a boolean the caller is free to ignore.
 *    Nothing about a run's outcome may depend on whether its picture reached
 *    R2. A run with no artifacts bucket runs identically to one with it.
 *
 * 2. **The feed never gates a run.** Every append happens inside a step the
 *    Workflow already checkpoints (the same `step.do` that publishes the
 *    board's own events), and a failed PUT is one console line, never a
 *    retried step and never an exception the run has to survive.
 *
 * 3. **No host paths, no operator identifiers.** A line carries ids, stages
 *    and human detail only — the same refusal the contract makes of durable
 *    records, for the same reason: the feed is readable from anywhere, so it
 *    must be publishable from anywhere.
 *
 * WHERE the lines live: R2 segment objects under the run's own artifact
 * prefix, exactly like the harness log stream (artifacts.ts) — R2 has no
 * append, so the stream is immutable segments a reader concatenates in key
 * order, which is what makes it followable WHILE the run is going. Segment
 * keys are allocated from the wave and batch indexes that already name the
 * steps, so a replayed Workflow (u9h restarts one mid-run on purpose) cannot
 * interleave a second copy of a line: a checkpointed step does not re-run,
 * and a re-run step overwrites its own key.
 */

import { runPrefix } from "./artifacts";
import type { Env } from "./index";

// ----------------------------------------------------------- the line schema ---

/**
 * One line of the feed: `ticfac.run_event.v1`, closed.
 *
 * `tick_id` and `attempt` are required-and-null rather than omittable — the
 * contract's own rule: a line that omits tick_id and one that states it as
 * null are different claims, and only the second is a claim at all. The
 * object literal below never uses `?:` for those two fields for that reason.
 */
export type FeedEvent = {
  schema_version: 1;
  /** RFC3339, stamped by the writer at the moment it did. */
  at: string;
  run_id: string;
  tick_id: string | null;
  /** 1-based; null only for run-level events. */
  attempt: number | null;
  /** A stage of the reconciler's own ordered vocabulary, never prose. */
  stage: string;
  detail: string;
};

/**
 * The reconciler's stage vocabulary, spelled where the cloud host uses it.
 *
 * These are the same strings `internal/reconcile` writes to the local feed —
 * imported by convention, not by module, because the Go side cannot be
 * imported here; the parity is what the tests pin (a cloud line with a stage
 * the local vocabulary does not hold would be a dashboard that shows a stage
 * no other host can produce).
 */
export const STAGE_DISPATCHED = "dispatched" as const;
export const STAGE_COLLECTED = "collected" as const;
export const STAGE_RUN_FINISHED = "run_finished" as const;

function line(input: {
  at?: string;
  run_id: string;
  tick_id: string | null;
  attempt: number | null;
  stage: string;
  detail: string;
}): FeedEvent {
  return {
    schema_version: 1,
    at: input.at ?? new Date().toISOString(),
    run_id: input.run_id,
    tick_id: input.tick_id,
    attempt: input.attempt,
    stage: input.stage,
    detail: input.detail,
  };
}

/** The run itself was dispatched onto the factory: a run-level line. */
export function runStartedFeedEvent(input: {
  run_id: string;
  detail: string;
  at?: string;
}): FeedEvent {
  return line({
    run_id: input.run_id,
    tick_id: null,
    attempt: null,
    stage: STAGE_DISPATCHED,
    detail: input.detail,
    ...(input.at === undefined ? {} : { at: input.at }),
  });
}

/**
 * A per-tick worker container is being dispatched: the same stage the local
 * reconciler writes at dispatch, with the tick and attempt on the line.
 */
export function tickDispatchedFeedEvent(input: {
  run_id: string;
  tick_id: string;
  attempt: number;
  detail: string;
  at?: string;
}): FeedEvent {
  return line({
    run_id: input.run_id,
    tick_id: input.tick_id,
    attempt: input.attempt,
    stage: STAGE_DISPATCHED,
    detail: input.detail,
    ...(input.at === undefined ? {} : { at: input.at }),
  });
}

/**
 * A per-tick worker container is finished, as COLLECT read it: the verdict is
 * the durable layer's, never the container's own say-so — the same rule the
 * board's `tickCompleted` states.
 */
export function tickCollectedFeedEvent(input: {
  run_id: string;
  tick_id: string;
  attempt: number;
  detail: string;
  at?: string;
}): FeedEvent {
  return line({
    run_id: input.run_id,
    tick_id: input.tick_id,
    attempt: input.attempt,
    stage: STAGE_COLLECTED,
    detail: input.detail,
    ...(input.at === undefined ? {} : { at: input.at }),
  });
}

/** The run reached a terminal state: the feed's own terminal line. */
export function runFinishedFeedEvent(input: {
  run_id: string;
  detail: string;
  at?: string;
}): FeedEvent {
  return line({
    run_id: input.run_id,
    tick_id: null,
    attempt: null,
    stage: STAGE_RUN_FINISHED,
    detail: input.detail,
    ...(input.at === undefined ? {} : { at: input.at }),
  });
}

/**
 * The supervisor could not ASK its orchestrator's container how the
 * orchestrator is doing (tick 3ed).
 *
 * Its own stage, deliberately a new word rather than a reuse: the local
 * reconciler never supervised an orchestrator container, and folding this
 * into any existing stage — a `run_finished`, a `held` — would be exactly
 * the collapse A9 forbids. A failed question is not a verdict about the
 * process (A2/A6), and the line must never read as one: the detail says the
 * question could not be asked, never that the orchestrator died.
 */
export const STAGE_ORCHESTRATOR_UNANSWERABLE = "orchestrator_unanswerable" as const;

/** The run-level line for a container the supervisor gave up asking (tick 3ed). */
export function orchestratorUnanswerableFeedEvent(input: {
  run_id: string;
  detail: string;
  at?: string;
}): FeedEvent {
  return line({
    run_id: input.run_id,
    tick_id: null,
    attempt: null,
    stage: STAGE_ORCHESTRATOR_UNANSWERABLE,
    detail: input.detail,
    ...(input.at === undefined ? {} : { at: input.at }),
  });
}

// ------------------------------------------------------------- the segments ---

/** Everything about one run's feed lives under this prefix, beside its logs. */
export function feedPrefix(project: string, runID: string): string {
  return `${runPrefix(project, runID)}feed/`;
}

/**
 * Widths chosen so lexicographic order is numeric order: a segment key that
 * sorted `10.jsonl` before `9.jsonl` would silently reorder the feed.
 */
const SEQ_WIDTH = 7;

function pad(value: number, width: number): string {
  return String(value).padStart(width, "0");
}

/**
 * Where one wave/batch site's segment lives, allocated from the indexes that
 * already name the step — wave and batch are what the step names carry, so
 * the key is recomputable on replay and stable across it.
 *
 * The spacing leaves room for the runs that exist: waves are capped far below
 * `MAX_RUN_WAVES`'s own ceiling and a batch's index below the per-wave slot.
 * A run beyond the guard is not refused — the append is skipped with one
 * console line, because the feed may never cost a run anything.
 */
export function waveFeedSeq(wave: number, batch: number, collected: boolean): number | null {
  if (wave < 0 || wave > 86 || batch < 0 || batch > 9998) return null;
  return 1 + wave * 100_000 + batch * 10 + (collected ? 2 : 1);
}

/** The run's first segment: the run-level started line. */
export const START_FEED_SEQ = 0;

/**
 * The one run-level segment a pass writes when it gives up asking its
 * orchestrator's container how it is (tick 3ed).
 *
 * Mid-range on purpose: above every wave segment's key so it can never
 * interleave one, below the terminal line so a subscriber still following
 * the run reads it before the finish. One fixed key per run rather than one
 * per boot — only one pass can end this way (a pass that fails here fails the
 * run, and only a tripped pass runs a closeout), and a replayed step rewrites
 * the same key rather than appending a second copy of the line.
 */
export const UNANSWERABLE_FEED_SEQ = 9_000_000;

/** The run's last segment: written at finalize, after every wave. */
export const FINAL_FEED_SEQ = 99_999_99;

export function feedSegmentKey(project: string, runID: string, seq: number): string {
  return `${feedPrefix(project, runID)}${pad(seq, SEQ_WIDTH)}.jsonl`;
}

// ----------------------------------------------------------------- the sink ---

/**
 * Appends events to the run's feed as one immutable segment. NEVER THROWS.
 *
 * The put is best effort by construction — caught, logged once per call, and
 * answered with `false`. A run whose feed could not be written is a run
 * nobody can watch live; it is not a run that failed, and no caller of this
 * function branches on the boolean to change what the run does next.
 */
export async function appendFeed(
  env: Env,
  input: {
    project: string;
    run_id: string;
    seq: number | null;
    events: FeedEvent[];
  },
): Promise<boolean> {
  if (input.seq === null || input.events.length === 0) return false;
  const bucket = env.ARTIFACTS;
  if (bucket === undefined || bucket === null) return false;
  const text = `${input.events.map((event) => JSON.stringify(event)).join("\n")}\n`;
  try {
    await bucket.put(feedSegmentKey(input.project, input.run_id, input.seq), text, {
      httpMetadata: { contentType: "application/x-ndjson; charset=utf-8" },
    });
    return true;
  } catch (error) {
    console.error(
      `factory run-feed: ${input.run_id} could not write feed segment ${input.seq}: ${String(
        error instanceof Error ? error.message : error,
      )}`,
    );
    return false;
  }
}

// ---------------------------------------------------------------- the reader ---

/**
 * The most bytes one page of the feed carries by default, and the most a
 * caller may ask for. A page is bounded so a long run's feed is never read
 * whole in one request: run_5c7c16d1 (epic hn6, ~5h, the orchestrator's feed
 * relayed every two seconds) left thousands of segments, and the route that
 * fetched every one of them, one R2 GET after another, on EVERY poll, never
 * answered inside the client's deadline — `ticfac events` and `ticfac status`
 * failed on it every time.
 */
export const FEED_PAGE_DEFAULT_BYTES = 512 * 1024;
export const FEED_PAGE_MAX_BYTES = 4 * 1024 * 1024;

/**
 * The most segments one page fetches, whatever their size: the per-request
 * subrequest budget is the Worker's, and a page of tiny segments must not
 * spend it all.
 */
export const FEED_PAGE_MAX_SEGMENTS = 256;

/** How many segment GETs a page has in flight at once. */
const FEED_READ_CONCURRENCY = 8;

/** One page of the feed, addressed by UTF-8 byte cursor. */
export type FeedPage = {
  /** The bytes of the feed from `from`, decoded: whole lines only. */
  text: string;
  /** The byte cursor the page starts at. */
  from: number;
  /** The UTF-8 bytes this page carries. */
  bytes: number;
  /** The cursor the next page starts at: `from + bytes`. */
  next: number;
  /** The feed's standing size in UTF-8 bytes. */
  total_bytes: number;
  /** Whether bytes stand past `next` that this page did not carry. */
  more: boolean;
};

/**
 * Where the page should start: a byte cursor a follower already holds, or
 * the last `tail` bytes of the feed (rounded back to the start of the
 * segment that holds that byte, so a tail read starts on a line boundary).
 */
export type FeedPageRequest = {
  from?: number;
  tail?: number;
  limit?: number;
};

type Segment = { key: string; size: number; start: number };

async function listFeedSegments(bucket: R2Bucket, prefix: string): Promise<Segment[]> {
  const listed: { key: string; size: number }[] = [];
  let cursor: string | undefined;
  for (;;) {
    const page: R2Objects = await bucket.list({
      prefix,
      ...(cursor === undefined ? {} : { cursor }),
    });
    for (const object of page.objects) listed.push({ key: object.key, size: object.size });
    if (!page.truncated) break;
    cursor = page.cursor;
  }
  listed.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
  const segments: Segment[] = [];
  let start = 0;
  for (const object of listed) {
    segments.push({ key: object.key, size: object.size, start });
    start += object.size;
  }
  return segments;
}

/**
 * One page of the run's feed — the R2 twin of reading
 * `.ticfac/logs/<run-id>/events.jsonl` from a byte offset: the segments ARE
 * the appends, so the concatenation in key order is the file, and a byte
 * cursor into it is what a follower resumes from.
 *
 * The LISTING carries every segment's size, so the cursor is placed without
 * fetching anything; only the segments the page carries are fetched, a few
 * at a time, and the page stops at `limit` bytes or FEED_PAGE_MAX_SEGMENTS
 * segments — so a request costs the same at hour five as at minute one.
 * A page always carries at least one whole segment when any stands past
 * `from`, so a follower can never be stuck behind one segment bigger than
 * its limit.
 */
export async function readRunFeedPage(
  bucket: R2Bucket,
  project: string,
  runID: string,
  request: FeedPageRequest = {},
): Promise<FeedPage> {
  const limit = Math.min(
    Math.max(request.limit ?? FEED_PAGE_DEFAULT_BYTES, 1),
    FEED_PAGE_MAX_BYTES,
  );
  const segments = await listFeedSegments(bucket, feedPrefix(project, runID));
  const total =
    segments.length === 0
      ? 0
      : segments[segments.length - 1]!.start + segments[segments.length - 1]!.size;

  let from = Math.min(Math.max(request.from ?? 0, 0), total);
  if (request.tail !== undefined) {
    const wanted = Math.max(total - Math.max(request.tail, 0), 0);
    // Back to the start of the segment holding that byte: segments end on a
    // line boundary, so a segment's start is always one.
    const holding = segments.find((segment) => wanted < segment.start + segment.size);
    from = holding === undefined ? total : holding.start;
  }

  const chosen: Segment[] = [];
  let planned = 0;
  for (const segment of segments) {
    if (segment.start + segment.size <= from) continue;
    if (chosen.length > 0 && (planned >= limit || chosen.length >= FEED_PAGE_MAX_SEGMENTS)) break;
    chosen.push(segment);
    planned += segment.start + segment.size - Math.max(from, segment.start);
  }

  const parts: Uint8Array[] = new Array(chosen.length);
  let index = 0;
  const worker = async (): Promise<void> => {
    for (;;) {
      const mine = index;
      index += 1;
      if (mine >= chosen.length) return;
      const segment = chosen[mine]!;
      const object = await bucket.get(segment.key);
      const body = object === null ? new Uint8Array() : new Uint8Array(await object.arrayBuffer());
      parts[mine] = segment.start < from ? body.subarray(from - segment.start) : body;
    }
  };
  await Promise.all(Array.from({ length: Math.min(FEED_READ_CONCURRENCY, chosen.length) }, worker));

  let bytes = 0;
  for (const part of parts) bytes += part.length;
  const joined = new Uint8Array(bytes);
  let at = 0;
  for (const part of parts) {
    joined.set(part, at);
    at += part.length;
  }
  const next = from + bytes;
  return {
    text: new TextDecoder().decode(joined),
    from,
    bytes,
    next,
    total_bytes: total,
    more: next < total,
  };
}

/**
 * The run's whole feed, oldest segment first: every page, walked by cursor.
 * For tests and small feeds; the route serves pages.
 */
export async function readRunFeed(
  bucket: R2Bucket,
  project: string,
  runID: string,
): Promise<string> {
  const chunks: string[] = [];
  let from = 0;
  for (;;) {
    const page = await readRunFeedPage(bucket, project, runID, { from });
    chunks.push(page.text);
    if (!page.more || page.bytes === 0) break;
    from = page.next;
  }
  return chunks.join("");
}

/**
 * How many times a tick has already been collected in this run: the honest
 * 1-based attempt number for the NEXT dispatch of that tick — a tick the
 * orchestrator re-requests in a later wave is attempt 2, not a second
 * attempt 1, and the identity on the line is the whole point of the line.
 */
export function attemptsSoFar(collectedTickIDs: readonly string[], tickID: string): number {
  let count = 0;
  for (const id of collectedTickIDs) {
    if (id === tickID) count += 1;
  }
  return count;
}
