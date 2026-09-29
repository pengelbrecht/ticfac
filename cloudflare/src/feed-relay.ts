/**
 * The orchestrator container's half of a cloud run's event feed.
 *
 * A cloud run's orchestrator is `ticfac run-epic` inside a container, and the
 * reconciler writes its whole journal — claimed, dispatched, gate_started,
 * repair_*, run_held — to `.ticfac/logs/<run>/events.jsonl` INSIDE that
 * container. Until this door existed none of it reached the factory's feed:
 * the first cloud run of epic hn6 (run_1960aa14…) ran 1h45m, gated, tried
 * three repairs and rebooted its orchestrator three times, and `ticfac
 * events` showed exactly two lines — the Workflow's start and its finish.
 *
 * So the container FOLLOWS that file (as the feed contract says a hosted
 * mirror must — a subscriber, never the run pushing) and relays what it reads
 * here, batched. The entrypoint relays its own lifecycle lines the same way,
 * so a boot that dies in its probes is not silent either.
 *
 * WHERE EACH BOOT'S LINES LIVE. Each boot of the orchestrator owns four slots
 * of the run's feed, in this order:
 *
 *   a. the Workflow's "boot N starting" line, written before the process is
 *      started — so nothing the container says can precede it;
 *   b. the entrypoint's lifecycle lines, relayed by the container (`boot`);
 *   c. the reconciler's own feed, relayed as it is written (`feed`);
 *   d. the Workflow's "boot N ended" line, with the exit code, written when
 *      the boot's watch ends.
 *
 * The keys sort after the run's start line (`0000000`) and before every
 * later run-level segment, so the concatenation a follower walks by byte
 * cursor is still only ever APPENDED to. That is also why a relayed segment
 * is refused once its boot's `d` line or the run's final line stands: a
 * segment slotted in before bytes a follower already read would move the
 * follower's cursor under it.
 *
 * IDEMPOTENT AND GAP-FREE. A relayed segment is keyed by the byte offset of
 * the container's stream it starts at, and the boot is the CREDENTIAL's (every
 * boot mints its own token, stamped with the boot number), never the
 * request's. A batch at an offset already stored is a replay and is answered
 * as stored; a batch at any other offset than where the stream stands is
 * refused with the offset expected, so a relay that lost its place resumes
 * exactly where the feed stands — never a line twice, never a hole.
 */

import { FEED_RELAY_PATH } from "./auth";
import { authorizeGatewayRequest, type GatewayDenial } from "./gateway";
import type { Env } from "./index";
import { type FeedEvent, FINAL_FEED_SEQ, feedPrefix, feedSegmentKey } from "./run-feed";

export { FEED_RELAY_PATH };

/** The two streams a container relays: the entrypoint's and the reconciler's. */
export type RelayStream = "boot" | "feed";
export const RELAY_STREAMS: readonly RelayStream[] = ["boot", "feed"];

/** The Workflow's own slots: `a` before a boot starts, `d` after it ended. */
export type BootSlot = "a" | "d";

const RELAY_SLOT: Record<RelayStream, "b" | "c"> = { boot: "b", feed: "c" };

const OFFSET_WIDTH = 12;
const BOOT_WIDTH = 4;
export const MAX_FEED_BOOT = 9_999;
export const MAX_RELAY_OFFSET = 999_999_999_999;

/**
 * The most one relayed batch may carry. A feed line is a few hundred bytes;
 * the relay batches far below this, and a request above it is not a batch.
 */
export const MAX_RELAY_BATCH_BYTES = 512 * 1024;

function pad(value: number, width: number): string {
  return String(value).padStart(width, "0");
}

/** Everything one boot of the orchestrator writes to the feed lives under this. */
export function bootFeedPrefix(project: string, runID: string, boot: number): string {
  return `${feedPrefix(project, runID)}0000001.boot${pad(boot, BOOT_WIDTH)}.`;
}

export function bootFeedKey(project: string, runID: string, boot: number, slot: BootSlot): string {
  return `${bootFeedPrefix(project, runID, boot)}${slot}.jsonl`;
}

export function relayFeedPrefix(
  project: string,
  runID: string,
  boot: number,
  stream: RelayStream,
): string {
  return `${bootFeedPrefix(project, runID, boot)}${RELAY_SLOT[stream]}.`;
}

export function relayFeedKey(
  project: string,
  runID: string,
  boot: number,
  stream: RelayStream,
  offset: number,
): string {
  return `${relayFeedPrefix(project, runID, boot, stream)}${pad(offset, OFFSET_WIDTH)}.jsonl`;
}

// ---------------------------------------------------- the Workflow's lines ---

/** The stage of the Workflow's line as it starts a boot of the orchestrator. */
export const STAGE_ORCHESTRATOR_BOOT = "orchestrator_boot" as const;
/** The stage of the Workflow's line when a boot's watch ended. */
export const STAGE_ORCHESTRATOR_EXIT = "orchestrator_exit" as const;

function runLevel(runID: string, stage: string, detail: string): FeedEvent {
  return {
    schema_version: 1,
    at: new Date().toISOString(),
    run_id: runID,
    tick_id: null,
    attempt: null,
    stage,
    detail,
  };
}

export function orchestratorBootFeedEvent(runID: string, detail: string): FeedEvent {
  return runLevel(runID, STAGE_ORCHESTRATOR_BOOT, detail);
}

export function orchestratorExitFeedEvent(runID: string, detail: string): FeedEvent {
  return runLevel(runID, STAGE_ORCHESTRATOR_EXIT, detail);
}

/**
 * Writes the Workflow's own line about one boot. NEVER THROWS — the feed is
 * exhaust, never authority, exactly as `appendFeed` states it.
 */
export async function appendBootFeed(
  env: Env,
  input: { project: string; run_id: string; boot: number; slot: BootSlot; event: FeedEvent },
): Promise<boolean> {
  const bucket = env.ARTIFACTS;
  if (bucket === undefined || bucket === null) return false;
  if (input.boot < 1 || input.boot > MAX_FEED_BOOT) return false;
  try {
    await bucket.put(
      bootFeedKey(input.project, input.run_id, input.boot, input.slot),
      `${JSON.stringify(input.event)}\n`,
      { httpMetadata: { contentType: "application/x-ndjson; charset=utf-8" } },
    );
    return true;
  } catch (error) {
    console.error(
      `factory feed-relay: ${input.run_id} could not write boot ${input.boot}'s ${input.slot} line: ${String(
        error instanceof Error ? error.message : error,
      )}`,
    );
    return false;
  }
}

// ------------------------------------------------------ the relayed lines ---

/**
 * The reconciler's terminal stages, renamed on the way into a CLOUD feed.
 *
 * Inside a container the reconciler's run_finished ends a BOOT, not the run:
 * the Workflow may reboot the orchestrator and the run goes on. Every
 * follower (`ticfac watch`, `status`) ends on run_finished / run_died, so a
 * relayed one would end a watch on boot 1 of a run that is still working.
 * The run's own terminal line on a cloud feed is the Workflow's (finalize),
 * and only the Workflow writes it.
 */
export const RELAYED_TERMINAL_STAGES: Readonly<Record<string, string>> = {
  run_finished: "orchestrator_finished",
  run_died: "orchestrator_died",
};

const FEED_LINE_KEYS = ["schema_version", "at", "run_id", "tick_id", "attempt", "stage", "detail"];
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

/**
 * One relayed line, checked against the closed `ticfac.run_event.v1` shape
 * before it may join a feed every follower parses strictly: a line a
 * follower refuses ends that follower's subscription, so the door refuses it
 * first. The run is the credential's, never the line's say-so.
 */
export function parseRelayedLine(
  raw: string,
  runID: string,
): { ok: true; event: FeedEvent } | { ok: false; detail: string } {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return { ok: false, detail: "a relayed line is not JSON" };
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return { ok: false, detail: "a relayed line is not a JSON object" };
  }
  const record = value as Record<string, unknown>;
  for (const key of Object.keys(record)) {
    if (!FEED_LINE_KEYS.includes(key)) {
      return {
        ok: false,
        detail: `a relayed line carries ${key}, which the closed schema does not`,
      };
    }
  }
  for (const key of FEED_LINE_KEYS) {
    if (!(key in record)) return { ok: false, detail: `a relayed line is missing ${key}` };
  }
  if (record.schema_version !== 1) {
    return { ok: false, detail: "a relayed line's schema_version is not 1" };
  }
  if (typeof record.at !== "string" || !RFC3339.test(record.at)) {
    return { ok: false, detail: "a relayed line's at is not RFC3339" };
  }
  if (record.run_id !== runID) {
    return {
      ok: false,
      detail: `a relayed line names run ${JSON.stringify(record.run_id)}, not the credential's ${runID}`,
    };
  }
  if (record.tick_id !== null && (typeof record.tick_id !== "string" || record.tick_id === "")) {
    return { ok: false, detail: "a relayed line's tick_id is neither a tick id nor null" };
  }
  if (
    record.attempt !== null &&
    (typeof record.attempt !== "number" || !Number.isInteger(record.attempt) || record.attempt < 1)
  ) {
    return { ok: false, detail: "a relayed line's attempt is neither a 1-based integer nor null" };
  }
  if (typeof record.stage !== "string" || record.stage === "") {
    return { ok: false, detail: "a relayed line names no stage" };
  }
  if (typeof record.detail !== "string") {
    return { ok: false, detail: "a relayed line's detail is not a string" };
  }
  return {
    ok: true,
    event: {
      schema_version: 1,
      at: record.at,
      run_id: runID,
      tick_id: record.tick_id as string | null,
      attempt: record.attempt as number | null,
      stage: RELAYED_TERMINAL_STAGES[record.stage] ?? record.stage,
      detail: record.detail,
    },
  };
}

/** Where one relayed stream stands: the container-side byte offset relayed so far. */
export async function relayedEnd(
  bucket: R2Bucket,
  project: string,
  runID: string,
  boot: number,
  stream: RelayStream,
): Promise<number> {
  const prefix = relayFeedPrefix(project, runID, boot, stream);
  let last: R2Object | undefined;
  let cursor: string | undefined;
  for (;;) {
    const page: R2Objects = await bucket.list({
      prefix,
      include: ["customMetadata"],
      ...(cursor === undefined ? {} : { cursor }),
    });
    for (const object of page.objects) {
      if (last === undefined || object.key > last.key) last = object;
    }
    if (!page.truncated) break;
    cursor = page.cursor;
  }
  if (last === undefined) return 0;
  return segmentEnd(last, 0);
}

function segmentEnd(object: R2Object, fallback: number): number {
  const end = Number(object.customMetadata?.end ?? Number.NaN);
  return Number.isSafeInteger(end) && end >= 0 ? end : fallback;
}

/** Why a boot's slice of the feed no longer takes relayed segments, or null. */
async function relayClosed(
  bucket: R2Bucket,
  project: string,
  runID: string,
  boot: number,
): Promise<string | null> {
  if ((await bucket.head(bootFeedKey(project, runID, boot, "d"))) !== null) {
    return `boot ${boot} has ended and its exit line stands`;
  }
  if ((await bucket.head(feedSegmentKey(project, runID, FINAL_FEED_SEQ))) !== null) {
    return "the run has finished and its final line stands";
  }
  return null;
}

export type RelayOutcome =
  | { ok: true; stored: boolean; end: number; lines: number }
  | { ok: false; status: number; error: string; detail: string; expected?: number };

/**
 * Appends one relayed batch: the bytes `[offset, offset + text)` of the
 * container's own stream, stored as one segment keyed by that offset.
 */
export async function appendRelayed(
  bucket: R2Bucket,
  input: {
    project: string;
    run_id: string;
    boot: number;
    stream: RelayStream;
    offset: number;
    text: string;
  },
): Promise<RelayOutcome> {
  const { project, run_id: runID, boot, stream, offset, text } = input;
  if (boot < 1 || boot > MAX_FEED_BOOT) {
    return {
      ok: false,
      status: 400,
      error: "invalid_request",
      detail: `boot ${boot} is out of range`,
    };
  }
  const closed = await relayClosed(bucket, project, runID, boot);
  if (closed !== null) {
    return {
      ok: false,
      status: 409,
      error: "feed_closed",
      detail: `${closed}; a segment written now would land before lines a follower already read`,
    };
  }

  const key = relayFeedKey(project, runID, boot, stream, offset);
  const existing = await bucket.head(key);
  if (existing !== null) {
    // A replay: the POST whose answer was lost. Answered as stored, never
    // written twice — the first write of an offset is the one that stands.
    return { ok: true, stored: false, end: segmentEnd(existing, offset), lines: 0 };
  }
  const expected = await relayedEnd(bucket, project, runID, boot, stream);
  if (offset !== expected) {
    return {
      ok: false,
      status: 409,
      error: "offset_mismatch",
      detail: `the ${stream} stream of boot ${boot} is relayed to byte ${expected}, not ${offset}`,
      expected,
    };
  }

  const events: FeedEvent[] = [];
  const lines = text.split("\n");
  // The last element is what follows the final newline: empty for a batch of
  // whole lines, which is the only kind the relay sends.
  if (lines.pop() !== "") {
    return {
      ok: false,
      status: 400,
      error: "invalid_line",
      detail: "a relayed batch must end on a newline: a partial line is still being written",
    };
  }
  for (const one of lines) {
    if (one.trim() === "") continue;
    const parsed = parseRelayedLine(one, runID);
    if (!parsed.ok) return { ok: false, status: 400, error: "invalid_line", detail: parsed.detail };
    events.push(parsed.event);
  }
  const end = offset + new TextEncoder().encode(text).length;
  const body = events.map((event) => `${JSON.stringify(event)}\n`).join("");
  await bucket.put(key, body, {
    httpMetadata: { contentType: "application/x-ndjson; charset=utf-8" },
    customMetadata: { end: String(end) },
  });
  return { ok: true, stored: true, end, lines: events.length };
}

// --------------------------------------------------------------- the door ---

function refuse(denial: GatewayDenial): Response {
  return Response.json({ error: denial.error, detail: denial.detail }, { status: denial.status });
}

function isStream(value: unknown): value is RelayStream {
  return typeof value === "string" && (RELAY_STREAMS as readonly string[]).includes(value);
}

/**
 * `POST /api/feed` appends a batch; `GET /api/feed?stream=feed|boot` answers
 * where that stream stands, so a relay (re)starting mid-boot resumes there.
 *
 * Authorized by the run's own gateway token, never the operator's: the caller
 * is the orchestrator container. Only the ORCHESTRATOR's credential may write
 * — a worker's token names its tick rather than the epic, and the feed
 * contract's boundary is that workers never write the run's feed.
 */
export async function feedRelayRoute(request: Request, env: Env): Promise<Response> {
  if (request.method !== "GET" && request.method !== "POST") {
    return Response.json(
      { error: "method_not_allowed", detail: "GET or POST" },
      { status: 405, headers: { Allow: "GET, POST" } },
    );
  }
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) return refuse(authorized.denial);
  const { token, run } = authorized;
  if (token.tick_id !== run.epic) {
    return refuse({
      status: 403,
      error: "not_orchestrator",
      detail: `the credential belongs to a worker of ${token.tick_id}; only the run's orchestrator relays its feed`,
    });
  }
  const bucket = env.ARTIFACTS;
  if (bucket === undefined || bucket === null) {
    return refuse({
      status: 503,
      error: "feed_unavailable",
      detail: "this deployment has no artifacts bucket, so there is no feed to relay into",
    });
  }
  const boot = token.attempt;

  if (request.method === "GET") {
    const stream = new URL(request.url).searchParams.get("stream") ?? "feed";
    if (!isStream(stream)) {
      return refuse({ status: 400, error: "invalid_request", detail: "stream is boot or feed" });
    }
    const end = await relayedEnd(bucket, run.project, run.run_id, boot, stream);
    return Response.json({ run_id: run.run_id, boot, stream, end });
  }

  const declared = Number(request.headers.get("content-length") ?? "0");
  if (declared > MAX_RELAY_BATCH_BYTES * 2) {
    return refuse({
      status: 413,
      error: "payload_too_large",
      detail: `a relayed batch is at most ${MAX_RELAY_BATCH_BYTES} bytes of feed`,
    });
  }
  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return refuse({ status: 400, error: "invalid_request", detail: "the body must be JSON" });
  }
  const raw = (body ?? {}) as Record<string, unknown>;
  if (!isStream(raw.stream)) {
    return refuse({ status: 400, error: "invalid_request", detail: "stream is boot or feed" });
  }
  const offset = raw.offset;
  if (
    typeof offset !== "number" ||
    !Number.isSafeInteger(offset) ||
    offset < 0 ||
    offset > MAX_RELAY_OFFSET
  ) {
    return refuse({
      status: 400,
      error: "invalid_request",
      detail: "offset is the stream's byte offset the batch starts at",
    });
  }
  if (typeof raw.text !== "string" || raw.text === "") {
    return refuse({
      status: 400,
      error: "invalid_request",
      detail: "text is one or more whole feed lines",
    });
  }
  if (new TextEncoder().encode(raw.text).length > MAX_RELAY_BATCH_BYTES) {
    return refuse({
      status: 413,
      error: "payload_too_large",
      detail: `a relayed batch is at most ${MAX_RELAY_BATCH_BYTES} bytes of feed`,
    });
  }

  const outcome = await appendRelayed(bucket, {
    project: run.project,
    run_id: run.run_id,
    boot,
    stream: raw.stream,
    offset,
    text: raw.text,
  });
  if (!outcome.ok) {
    return Response.json(
      {
        error: outcome.error,
        detail: outcome.detail,
        ...(outcome.expected === undefined ? {} : { expected: outcome.expected }),
      },
      { status: outcome.status },
    );
  }
  return Response.json(
    { stored: outcome.stored, boot, end: outcome.end, lines: outcome.lines },
    { status: outcome.stored ? 201 : 200 },
  );
}
