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
 * The run's feed so far, oldest segment first — the R2 twin of opening
 * `.ticfac/logs/<run-id>/events.jsonl`: the segments ARE the appends, so the
 * concatenation in key order is the file.
 *
 * No truncation, unlike the harness tail: a feed line is bounded by the
 * run's own structure (one per dispatch, collect and terminal event), so the
 * whole feed is what a subscriber follows from, and a read that bounded it
 * from the end would hand a follower a cursor it cannot resume from.
 */
export async function readRunFeed(
  bucket: R2Bucket,
  project: string,
  runID: string,
): Promise<string> {
  const prefix = feedPrefix(project, runID);
  const keys: string[] = [];
  let cursor: string | undefined;
  for (;;) {
    const page: R2Objects = await bucket.list({
      prefix,
      ...(cursor === undefined ? {} : { cursor }),
    });
    keys.push(...page.objects.map((object) => object.key));
    if (!page.truncated) break;
    cursor = page.cursor;
  }
  keys.sort();

  const chunks: string[] = [];
  for (const key of keys) {
    const object = await bucket.get(key);
    if (object !== null) chunks.push(await object.text());
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
