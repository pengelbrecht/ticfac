/**
 * The status half of "follow ticfac from a phone" (ticfac tick i1r): the
 * factory stores and serves the 6dh status model — `ticfac.status.v1`, the one
 * model every surface renders — for EVERY run it can see.
 *
 * Two ways a run reaches this factory, and they are deliberately not the same
 * mechanism:
 *
 *  - A CLOUD run is reported NATIVELY. The factory already holds its index
 *    row (`runs`), its progress verdict (`run_progress`) and its room's
 *    pending gates, so [cloudStatusDoc] composes a model at read time from
 *    records other code wrote. No push, no new source of truth.
 *
 *  - A LOCAL run is not on this machine: nothing here can read its tracker,
 *    its `.ticfac/` records or its event feed. So the run itself pushes small
 *    snapshots while it works — `ticfac run-epic`'s pusher, opted in by a
 *    ticfac config flag (`factory_status_push` in ~/.ticfacrc) — and
 *    [saveStatusSnapshot] stores the last one per run. A snapshot carries no
 *    secrets and no work product, only the model and the label map the page
 *    names ticks by.
 *
 * This module is a READER of the model, never a second opinion: the fields it
 * types are the fields the phone page and the alert evaluator read, and the
 * model's own shape is pinned in the contract bundle this factory reads too
 * (contracts/status-model.json, moved in from the Go package when this second
 * reader arrived — tick 4i8; cloudflare/test/status-model.test.ts runs the
 * same goldens and refusals the Go readers run). A document whose
 * `schema_version` is not 1 is refused rather than guessed at — the rule
 * every versioned surface here holds.
 */

import type { Run, RunProgressRecord } from "./db";
import type { PendingEntry, RunEventView } from "./run-room";

/**
 * The model's own identity, `ticfac.status.v1` — the same schema id the Go
 * side's `statusmodel.SchemaID` spells, and the value a document carries in
 * `schema_version` numbering that this reader knows.
 */
export const STATUS_SCHEMA_VERSION = 1;

/** The pushed-envelope shape's version. Moves only with the envelope. */
export const SNAPSHOT_ENVELOPE_VERSION = 1;

/**
 * How old a local run's snapshot may be before the page says PAUSED/STALE.
 *
 * The pusher's cadence is 30 seconds, so three missed pushes are either a
 * network that dropped them or a laptop that slept — and a sleeping laptop is
 * the case the page exists FOR (the operator closed it and went outside).
 * The page must say "paused or stale, reading is old" rather than render a
 * stopped clock as a live run, which would look stuck.
 */
export const LOCAL_SNAPSHOT_STALE_MS = 90_000;

/** The largest snapshot this door accepts. "Small" is a promise the page makes. */
export const MAX_SNAPSHOT_BYTES = 256 * 1024;

/** One needs-a-person entry, the model's own `attention` row. */
export type StatusAttention = {
  kind: string;
  what: string;
  since?: string | null;
  needs_person?: boolean;
  unblock_command?: string | null;
};

/**
 * One stop of a tick's pipeline cell — the stages this role's tick passes
 * through, each with its own state (hn6, wave 1). A stage the tick has not
 * reached reads `pending`; the cell fills left to right and a renderer
 * draws one glyph per stage.
 */
export type StatusPipelineStage = { stage: string; state: string };

/**
 * One try of a tick's attempt history (hn6, wave 1): the outcome the durable
 * records state, the tier it dispatched at, and — for a try the records
 * refused — the reason the feed states and, on the last try only, the run's
 * own next step. All optional because a pre-hn6 snapshot carries none of
 * them and every field the records do not state stays null.
 */
export type StatusTry = {
  try?: number;
  attempt?: number;
  outcome?: string;
  tier?: string | null;
  reason?: string | null;
  next_step?: string | null;
  dispatched_at?: string;
};

/**
 * One tick of the model's wave listing. The pre-hn6 shape — id, title,
 * state — stays; the hn6 fields (gloss, the pipeline cell, the parent row,
 * the duration, the tries) are optional because older snapshots lack them.
 */
export type StatusTick = {
  tick_id: string;
  title?: string;
  gloss?: string;
  state: string;
  pipeline?: StatusPipelineStage[];
  parent_tick_id?: string | null;
  duration_seconds?: number | null;
  tries?: StatusTry[];
  /** The tick this one duplicates, when the tracker closed it as a later
   *  promotion of the same finding — the work is the named tick's. Null on
   *  every tick that is its own work (hn6, tick gmo). */
  duplicate_of?: string | null;
};

/**
 * One cost line (hn6, wave 2): which river the spend ran through, whether
 * anything measured it, the number where one exists. An unmetered line
 * carries usd null and says so — never a fabricated $0.00.
 */
export type StatusCostLine = {
  source: string;
  metered: boolean;
  usd?: number | null;
  attempts?: number;
  basis?: string | null;
};

/** One feed event of the model's recent tail — the run's own last words. */
export type StatusRecentEvent = {
  at: string;
  tick_id?: string | null;
  attempt?: number | null;
  stage: string;
  detail: string;
};

/**
 * The fields of `ticfac.status.v1` this factory reads.
 *
 * The hn6 dashboard fields (progress, remaining, health.verdict, the
 * lifecycle's own phases, the per-tick pipeline cell and try history, the
 * cost lines, the recent tail) are ALL optional: an older snapshot carries
 * none of them and the cloud-composed doc carries only what the factory's
 * own records state, and a reader that guessed at an absent field would be
 * a second opinion, not a reader. A doc without them renders exactly as it
 * did before they existed.
 */
export type StatusDoc = {
  schema_version: number;
  run_id: string;
  epic_id: string;
  host: string;
  generated_at: string;
  liveness: {
    alive: boolean;
    state: string;
    reason: string;
  };
  lifecycle: {
    phase: string;
    /** Each phase's own state (plan, waves, review, close-out, ci, merge). */
    phases?: { phase: string; state: string }[];
    wave?: { active: number; total: number } | null;
  };
  attention: StatusAttention[];
  /**
   * The ordinary wait (what a live run is doing), from the model's own
   * `waits_on`. Null when nothing blocks the run.
   */
  waits_on?: { kind: string; what: string; needs_person?: boolean } | null;
  /**
   * Every wave with its ticks, when the run's tracker was readable. Null is
   * the model's own word for "unread", not "empty".
   */
  waves?:
    | {
        wave: number;
        state: string;
        ticks: StatusTick[];
      }[]
    | null;
  /** The epic's children and waves counted; null when the tracker was unreadable. */
  progress?: {
    ticks?: { total: number; closed: number; open?: number } | null;
    waves?: { total: number; done: number; active: number } | null;
    /**
     * The epic's own clock (tick e6g): the earliest dispatch the try
     * history states, to generated_at — clamped at the run's end when the
     * run's own records state one, so a finished run's clock stops. The
     * same field the terminal header renders; null when no dispatch
     * marker states a start.
     */
    run_elapsed_seconds?: number | null;
  } | null;
  /**
   * The approximate time left, only where measured tick durations support
   * it — the ETA the dashboard shows as `~`. Null everywhere else.
   */
  remaining?: { approximate_seconds?: number | null; basis?: string } | null;
  /**
   * The health counts and — the hn6 addition — the verdict: the headline a
   * dashboard answers "is it healthy" with, so a person reads a word rather
   * than four counters. Its presence is the page's marker for a doc built
   * after hn6: the model made the verdict required, so every hn6 snapshot
   * carries one and every older snapshot carries none.
   */
  health?: {
    remote_retries?: number;
    interventions?: number;
    stall_warnings?: number;
    wall_clocks_fired?: number;
    verdict?: {
      state: string;
      summary?: string;
      recovered?: { what: string; count: number; seconds?: number | null }[];
    };
  };
  /** The spend split per source; `lines` is what a renderer draws. */
  cost?: {
    recorded_usd?: number | null;
    attempts?: number;
    basis?: string | null;
    lines?: StatusCostLine[];
  } | null;
  /** The run's own last words: the tail of its feed, oldest first. */
  recent?: StatusRecentEvent[];
};

/** What a local run pushes: the model, plus the labels the page names ticks by. */
export type StatusSnapshotEnvelope = {
  schema_version: number;
  run_id: string;
  host: string;
  pushed_at: string;
  model: StatusDoc;
  tick_labels?: Record<string, string>;
};

/** A parsed status snapshot as the store keeps it. */
export type StoredSnapshot = {
  run_id: string;
  host: string;
  epic_id: string;
  pushed_at: string;
  model: StatusDoc;
  tick_labels: Record<string, string> | null;
};

export type EnvelopeParse =
  | { ok: true; envelope: StatusSnapshotEnvelope }
  | { ok: false; status: number; error: string; detail: string };

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Parses and refuses a pushed snapshot. Every refusal names what a pusher can
 * fix: this is the door a run's own pusher talks to, so the contract between
 * the two halves of this feature is what this function checks — envelope
 * version, host, the model's own schema version, and the run identity the
 * model carries matching the envelope's.
 */
export function parseSnapshotEnvelope(body: unknown, byteLength: number): EnvelopeParse {
  const refuse = (status: number, error: string, detail: string): EnvelopeParse => ({
    ok: false,
    status,
    error,
    detail,
  });
  if (byteLength > MAX_SNAPSHOT_BYTES) {
    return refuse(
      413,
      "snapshot_too_large",
      `a status snapshot is bounded at ${MAX_SNAPSHOT_BYTES} bytes; got ${byteLength}`,
    );
  }
  if (!isObject(body)) return refuse(400, "invalid_request", "the snapshot must be a JSON object");

  if (body.schema_version !== SNAPSHOT_ENVELOPE_VERSION) {
    return refuse(
      422,
      "unsupported_version",
      `snapshot envelope schema_version must be ${SNAPSHOT_ENVELOPE_VERSION}; got ${String(body.schema_version)}`,
    );
  }
  if (body.host !== "local") {
    return refuse(
      400,
      "invalid_request",
      `this door stores LOCAL runs' snapshots (host "local"); a cloud run needs none — the factory composes its status natively`,
    );
  }
  const runID = body.run_id;
  if (typeof runID !== "string" || runID.trim() === "" || runID.length > 64) {
    return refuse(400, "invalid_request", "run_id must be a non-empty string of at most 64 chars");
  }
  const pushedAt = body.pushed_at;
  if (typeof pushedAt !== "string" || Number.isNaN(Date.parse(pushedAt))) {
    return refuse(400, "invalid_request", "pushed_at must be an RFC3339 timestamp");
  }
  const model = body.model;
  if (!isObject(model)) return refuse(400, "invalid_request", "model is required");
  if (model.schema_version !== STATUS_SCHEMA_VERSION) {
    return refuse(
      422,
      "unsupported_version",
      `model schema_version must be ${STATUS_SCHEMA_VERSION} (ticfac.status.v1); got ${String(model.schema_version)}`,
    );
  }
  if (model.run_id !== runID) {
    return refuse(
      400,
      "invalid_request",
      `the envelope names run ${runID} but its model names ${String(model.run_id)}`,
    );
  }
  if (model.host !== "local") {
    return refuse(
      400,
      "invalid_request",
      `the model says host ${String(model.host)}; only a local run pushes snapshots`,
    );
  }
  const labels = body.tick_labels;
  if (labels !== undefined && labels !== null) {
    if (!isObject(labels)) {
      return refuse(400, "invalid_request", "tick_labels must be an object of id to label");
    }
    const entries = Object.entries(labels);
    if (entries.length > 64) {
      return refuse(400, "invalid_request", "tick_labels is bounded at 64 entries");
    }
    for (const [id, label] of entries) {
      if (typeof label !== "string" || label.length > 200) {
        return refuse(
          400,
          "invalid_request",
          `tick_labels["${id}"] must be a string of at most 200 chars`,
        );
      }
    }
  }

  return {
    ok: true,
    envelope: {
      schema_version: SNAPSHOT_ENVELOPE_VERSION,
      run_id: runID,
      host: "local",
      pushed_at: pushedAt,
      model: model as unknown as StatusDoc,
      ...(labels === undefined || labels === null
        ? {}
        : { tick_labels: labels as Record<string, string> }),
    },
  };
}

// ------------------------------------------------------------ the store ---

/**
 * Stores one local run's last snapshot. An UPSERT, not an append: the page
 * answers "where is the run now", and the run's own feed (which the factory
 * also streams for cloud runs, tick k7p) is the history.
 */
export async function saveStatusSnapshot(db: D1Database, snapshot: StoredSnapshot): Promise<void> {
  await db
    .prepare(
      `INSERT INTO status_snapshots (run_id, host, epic_id, pushed_at, model, tick_labels)
       VALUES (?, ?, ?, ?, ?, ?)
       ON CONFLICT (run_id) DO UPDATE SET
         host = excluded.host,
         epic_id = excluded.epic_id,
         pushed_at = excluded.pushed_at,
         model = excluded.model,
         tick_labels = excluded.tick_labels`,
    )
    .bind(
      snapshot.run_id,
      snapshot.host,
      snapshot.epic_id,
      snapshot.pushed_at,
      JSON.stringify(snapshot.model),
      snapshot.tick_labels === null ? null : JSON.stringify(snapshot.tick_labels),
    )
    .run();
}

/** Every stored local snapshot, newest push first. */
export async function listStatusSnapshots(db: D1Database): Promise<StoredSnapshot[]> {
  const rows = await db
    .prepare(
      "SELECT run_id, host, epic_id, pushed_at, model, tick_labels FROM status_snapshots ORDER BY pushed_at DESC",
    )
    .all<{
      run_id: string;
      host: string;
      epic_id: string;
      pushed_at: string;
      model: string;
      tick_labels: string | null;
    }>();
  return (rows.results ?? []).map((row) => ({
    run_id: row.run_id,
    host: row.host,
    epic_id: row.epic_id,
    pushed_at: row.pushed_at,
    model: JSON.parse(row.model) as StatusDoc,
    tick_labels:
      row.tick_labels === null ? null : (JSON.parse(row.tick_labels) as Record<string, string>),
  }));
}

// ------------------------------------------------- the cloud composition ---

/**
 * Composes a cloud run's status document from the factory's own records.
 *
 * Honest by construction, in the model's own vocabulary:
 *  - `waves` is absent — the worker cannot read the epic's tracker, and the
 *    model's rule is that an unreadable source costs the field, never a guess.
 *  - attention is the room's PENDING GATES mapped one to one: a pending gate
 *    is the one thing a CLOUD run stops for that needs a person, and its own
 *    existing delivery (the Telegram question, when the orchestrator asked
 *    with notify=telegram) is what carries the answer back.
 *  - a FAILED run carries the resume as its clearing command, in the same
 *    words the operator's own tick spells: stop a run, edit the tracker, and
 *    submit it again.
 *
 * `lastEvent` is the room's forwarded tail the observe read already draws
 * (tick bne); null means the room held nothing, and the document says no
 * reason rather than echoing the state word it already said.
 *
 * `cost` is the one hn6 dashboard field the factory's own records can state
 * for a cloud run: the measured Workers AI spend the run row carries, as one
 * metered line named for where the number came from — and METERED ONLY WHEN
 * the row says its cost_source is the gateway (tick 1tm): the runs row's
 * cost_usd is NOT NULL DEFAULT 0, so a run before its first cost sync, or
 * one whose telemetry could not be read, carries a default rather than a
 * measurement, and its line says "not metered" and states no number, the
 * same basis the Go model's cloud line names. The rest of the dashboard
 * fields stay absent — the composed doc claims nothing the
 * factory's records do not state — and `waves` stays null.
 */
export function cloudStatusDoc(
  run: Run,
  gates: PendingEntry[],
  progress: RunProgressRecord | null,
  lastEvent: RunEventView | null,
): StatusDoc {
  const alive = run.state === "starting" || run.state === "running" || run.state === "stopping";
  const phase =
    run.state === "completed"
      ? "done"
      : run.state === "failed"
        ? "failed"
        : run.state === "stopped"
          ? "cancelled"
          : "waves";

  const attention: StatusAttention[] = gates
    .filter((gate) => gate.epic === run.epic)
    .map((gate) => ({
      kind: "held-for-person",
      what: gate.question.text,
      needs_person: true,
      unblock_command: null,
    }));

  const reason =
    lastEvent === null || lastEvent.message === null
      ? progress === null
        ? ""
        : progress.detail
      : lastEvent.message;

  return {
    schema_version: STATUS_SCHEMA_VERSION,
    run_id: run.run_id,
    epic_id: run.epic,
    host: "cloud",
    generated_at: new Date().toISOString(),
    liveness: {
      alive,
      state: run.state,
      reason: run.state === "failed" ? reason : alive ? "" : reason,
    },
    lifecycle: { phase },
    attention,
    waits_on:
      alive && attention.length === 0
        ? { kind: "workers", what: "the orchestrator container is working" }
        : null,
    waves: null,
    cost: {
      lines: [
        run.cost_source === "gateway"
          ? {
              source: "workers-ai",
              metered: true,
              usd: run.cost_usd,
              basis: "AI Gateway logs",
            }
          : {
              // The unsynced record: the row's number is the schema's
              // default, not a measurement, so the line states no number and
              // names the telemetry — the same basis the Go model's cloud
              // line carries, so the two renderers cannot disagree.
              source: "workers-ai",
              metered: false,
              usd: null,
              basis: "not metered: the gateway's cost telemetry has not answered for this run",
            },
      ],
    },
  };
}

// -------------------------------------------------- the shared classifier ---

/** The closed classification vocabulary: the same four answers `ticfac` (bare, tick 2qz) renders. */
export type RunClass = "held" | "failed" | "running" | "done" | "cancelled";

/**
 * The one command that resumes a stopped run, named by the host the run lives
 * on — this reader's mirror of the Go model's `statusmodel.ResumeCommand`
 * (tick gtk, and this tick tt6, which stopped the TS spellings from ignoring
 * the doc's host). A LOCAL run's resume is the foreground form the
 * reconciler itself runs (`run-epic` — `ticfac run` starts the same command
 * in the background and attaches to it). A CLOUD run's resume is a NEW
 * SUBMISSION to its factory — `ticfac run <epic> --cloud` — because nothing
 * on the machine reading the model can restart the factory's Workflow except
 * the factory, and `run-epic` here would restart the epic LOCALLY, in the
 * foreground, on whatever machine happens to be reading: the same stop, a
 * different run.
 */
export function resumeCommand(host: string, epicID: string): string {
  if (host === "cloud") {
    return `ticfac run ${epicID} --cloud`;
  }
  return `ticfac run-epic ${epicID}`;
}

/**
 * Classifies one run's document into the listing's row — the same read of the
 * model the bare `ticfac` overview performs (attention first, a live
 * incarnation next, the terminal phases last), ported so the phone page and
 * the terminal answer the same question with the same words.
 *
 * The reason and the one clearing command come from the MODEL, never from a
 * second opinion the page would have to keep in agreement.
 */
export function classifyStatusDoc(doc: StatusDoc): {
  state: RunClass;
  reason: string;
  clear_with: string | null;
} {
  let primary = primaryAttention(doc.attention);
  if (primary !== null && primary.kind === "dead-run" && doc.lifecycle.phase === "failed") {
    // The failed row IS that wait (tick jkb): a run whose own word says it
    // failed now carries its resume in attention, and the row says what it
    // is — failed, with the same resume — in the failed class's own
    // colour, the same carve-out the bare `ticfac` overview holds for the
    // orphaned cloud record. A held band is for stops a person must clear
    // before anything else; the resume is exactly the move the failed row
    // already names.
    primary = null;
  }
  if (primary !== null) {
    return {
      state: "held",
      reason: primary.what,
      clear_with: primary.unblock_command ?? null,
    };
  }
  if (doc.liveness.alive) {
    const wait = doc.waits_on;
    if (wait !== null && wait !== undefined && wait.what !== "") {
      return { state: "running", reason: wait.what, clear_with: null };
    }
    return { state: "running", reason: "working", clear_with: null };
  }
  switch (doc.lifecycle.phase) {
    case "failed":
      return {
        state: "failed",
        reason: doc.liveness.reason,
        // The resume is the host's, not always the local foreground form: a
        // failed cloud run's page must not send the person to restart the
        // epic on their own machine (tick tt6).
        clear_with: resumeCommand(doc.host, doc.epic_id),
      };
    case "cancelled":
      return { state: "cancelled", reason: doc.liveness.reason, clear_with: null };
    default:
      return { state: "done", reason: doc.liveness.reason, clear_with: null };
  }
}

/**
 * Picks the attention entry a held run's line answers with, by the same
 * hardness order the bare `ticfac` overview ranks: the run's own hold first,
 * then an untriaged finding, then a dead run, then the merge. An unknown kind
 * is honest last, never dropped.
 */
export function primaryAttention(entries: StatusAttention[]): StatusAttention | null {
  let best: StatusAttention | null = null;
  let bestRank = Number.MAX_SAFE_INTEGER;
  for (const entry of entries) {
    if (entry.needs_person === false) continue;
    const rank = attentionRank(entry.kind);
    if (rank < bestRank) {
      best = entry;
      bestRank = rank;
    }
  }
  return best;
}

function attentionRank(kind: string): number {
  switch (kind) {
    case "held-for-person":
      return 0;
    case "finding":
      return 1;
    case "dead-run":
      return 2;
    case "merge":
      return 3;
    default:
      return 4;
  }
}

/** The listing's band order: held first, failed next, running after, terminal last. */
export function classRank(state: string): number {
  switch (state) {
    case "held":
      return 0;
    case "failed":
      return 1;
    case "running":
      return 2;
    case "cancelled":
      return 3;
    default:
      return 4;
  }
}

/**
 * Names a tick the way every operator surface names one (tick q90): "id
 * (label)", or the bare id when no label is known.
 */
export function tickRef(id: string, labels: Record<string, string> | null | undefined): string {
  const label = labels?.[id];
  return label === undefined || label === "" ? id : `${id} (${label})`;
}
