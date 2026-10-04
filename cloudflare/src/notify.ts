/**
 * The alert half of "follow ticfac from a phone" (ticfac tick i1r): Telegram
 * notifications through the EXISTING operator channel — the same bot, the
 * same paired chat `telegram.ts` serves — for the three things an unattended
 * factory must page a person for:
 *
 *  1. **needs a person** — a run whose status model carries attention, with
 *     the reason and the ONE command that clears it;
 *  2. **epic done**;
 *  3. **run failed** — with the resume as the clearing command.
 *
 * ## Deduplicated per stop, not per poll
 *
 * A local run pushes a snapshot every 30 seconds and the same stop is in many
 * of them. The evaluator therefore compares STOPS, not pictures: a stop is
 * identified by (run, kind, reason) and remembered in `status_alerts` until it
 * clears. The first push that carries a stop sends the message; every later
 * push that carries the same stop sends nothing. A stop that clears and comes
 * back is a new episode and pages again — a stop that returns is news.
 *
 * The same table is written from the Run Workflow's finalize (tick i1r), so a
 * retried Workflow step cannot page twice for one ending either: "the run
 * finished" and "the run failed" are stops like any other.
 *
 * ## What a message must carry
 *
 * Everything this module composes is derived from the status model, never
 * re-derived from records: the reason is the attention entry's own `what`, the
 * clearing command is the model's own `unblock_command`, and a tick named in
 * the message is named "id (label)" (tick q90) from the label map the snapshot
 * pushed beside the model.
 */

import { getEnrolledProject } from "./db";
import type { Env } from "./index";
import type { StatusDoc } from "./status";
import { primaryAttention, resumeCommand } from "./status";
import { escapeHTML, sendTelegramHTML } from "./telegram";

/** The minimum gap between two sends for one run. Batches, never suppresses: a deferred send waits for the next evaluation. */
export const MIN_ALERT_GAP_MS = 60_000;

/** One thing a person must do (or must know), as the evaluator sees it. */
export type StatusStop = {
  /** Identity within the run: "person:<kind>:<what>", "terminal:done", "terminal:failed:<reason>". */
  key: string;
  kind: "person" | "done" | "failed";
  what: string;
  clear_with: string | null;
};

/** The widest a stop key's prose half may be — bounded so the row key stays a key. */
const KEY_PROSE_LIMIT = 200;

function keyProse(text: string): string {
  const flat = text.replaceAll("\n", " ").trim();
  return flat.length > KEY_PROSE_LIMIT ? flat.slice(0, KEY_PROSE_LIMIT) : flat;
}

/**
 * The stops a status document states. Pure: the same document yields the same
 * stops, which is what makes "per stop, not per poll" decidable.
 *
 * A done run that still carries attention does not get a done stop: the merge
 * (or the finding) IS the news, and it pages in its own word; "the run
 * finished" on top of it would be a second message about the same person's
 * move.
 */
export function stopsFromStatusDoc(doc: StatusDoc): StatusStop[] {
  const stops: StatusStop[] = [];
  const primary = primaryAttention(doc.attention);
  if (primary !== null) {
    stops.push({
      key: `person:${primary.kind}:${keyProse(primary.what)}`,
      kind: "person",
      what: primary.what,
      clear_with: primary.unblock_command ?? null,
    });
  }
  if (!doc.liveness.alive) {
    if (doc.lifecycle.phase === "failed") {
      const reason = doc.liveness.reason === "" ? "the run failed" : doc.liveness.reason;
      stops.push({
        key: `terminal:failed:${keyProse(reason)}`,
        kind: "failed",
        what: reason,
        // The resume is named by the host the run lives on, the Go model's
        // own answer (statusmodel.ResumeCommand): a failed cloud run's page
        // must not name `run-epic` — the local foreground form that would
        // restart the epic on the reader's machine (tick tt6).
        clear_with: resumeCommand(doc.host, doc.epic_id),
      });
    } else if (doc.lifecycle.phase === "done" && primary === null) {
      const reason =
        doc.liveness.reason === "" ? "the run finished its own work" : doc.liveness.reason;
      stops.push({
        key: "terminal:done",
        kind: "done",
        what: reason,
        clear_with: null,
      });
    }
  }
  return stops;
}

export type AlertEvaluation = {
  /** Stops newly paged this evaluation. */
  sent: number;
  /** Stops open and awaiting their first send (rate limit, or Telegram could not be reached). */
  pending: number;
  /** Stops that cleared since the last evaluation. */
  cleared: number;
};

/**
 * Evaluates one run's stops against its alert memory, sends what is new, and
 * clears what is gone. Never throws: an evaluator that could not read or send
 * records its failure and answers — a broken notifier must never become a
 * broken status door (the page keeps working on stale data, labelled stale,
 * and the next evaluation retries what this one could not deliver).
 */
export async function evaluateStatusAlerts(
  env: Env,
  input: {
    run_id: string;
    doc: StatusDoc;
    tick_labels?: Record<string, string> | null;
    topic_id?: string;
    now?: Date;
  },
): Promise<AlertEvaluation> {
  const now = input.now ?? new Date();
  const stops = stopsFromStatusDoc(input.doc);
  const db = env.DB;

  const rows = await db
    // Only UNCleared rows count as open: a cleared row is a past episode, and
    // a stop that returns after one starts a new episode and pages again.
    .prepare(
      "SELECT stop_key, notified_at FROM status_alerts WHERE run_id = ? AND cleared_at IS NULL",
    )
    .bind(input.run_id)
    .all<{ stop_key: string; notified_at: string | null }>();
  const open = new Map((rows.results ?? []).map((row) => [row.stop_key, row.notified_at]));

  // A stop that is gone clears its row; a stop that returns after a clear is
  // a new episode (the row is reset and re-notified).
  let cleared = 0;
  for (const key of open.keys()) {
    if (!stops.some((stop) => stop.key === key)) {
      await db
        .prepare("UPDATE status_alerts SET cleared_at = ? WHERE run_id = ? AND stop_key = ?")
        .bind(now.toISOString(), input.run_id, key)
        .run();
      cleared += 1;
    }
  }

  // New stops: absent rows, or rows whose send never landed. A row that was
  // notified is never re-sent, no matter how many more pushes carry it.
  const fresh = stops.filter(
    (stop) => open.get(stop.key) === undefined || open.get(stop.key) === null,
  );
  let sent = 0;
  let pending = 0;
  if (fresh.length > 0) {
    const rateOk = await withinRateLimit(db, input.run_id, now);
    for (const stop of fresh) {
      await db
        .prepare(
          `INSERT INTO status_alerts (run_id, stop_key, kind, what, clear_with, opened_at)
           VALUES (?, ?, ?, ?, ?, ?)
           ON CONFLICT (run_id, stop_key) DO UPDATE SET
             kind = excluded.kind,
             what = excluded.what,
             clear_with = excluded.clear_with,
             opened_at = excluded.opened_at,
             notified_at = NULL,
             cleared_at = NULL`,
        )
        .bind(input.run_id, stop.key, stop.kind, stop.what, stop.clear_with, now.toISOString())
        .run();
      if (!rateOk) {
        pending += 1;
        continue;
      }
      const delivered = await sendStopAlert(
        env,
        input.run_id,
        input.doc.epic_id,
        stop,
        input.tick_labels,
        input.topic_id,
      );
      if (delivered) {
        await db
          .prepare("UPDATE status_alerts SET notified_at = ? WHERE run_id = ? AND stop_key = ?")
          .bind(now.toISOString(), input.run_id, stop.key)
          .run();
        sent += 1;
      } else {
        pending += 1;
      }
    }
  }
  return { sent, pending, cleared };
}

/** Whether a send for this run may go out now (one conversation per run, not a burst). */
async function withinRateLimit(db: D1Database, runID: string, now: Date): Promise<boolean> {
  const row = await db
    .prepare(
      "SELECT MAX(notified_at) AS last FROM status_alerts WHERE run_id = ? AND notified_at IS NOT NULL",
    )
    .bind(runID)
    .first<{ last: string | null }>();
  if (row === null || row.last === null) return true;
  return now.getTime() - Date.parse(row.last) >= MIN_ALERT_GAP_MS;
}

/**
 * Sends one stop's message. Returns false — rather than throwing — when the
 * channel cannot be reached, so the stop stays pending and the next
 * evaluation retries it.
 */
async function sendStopAlert(
  env: Env,
  runID: string,
  epicID: string,
  stop: StatusStop,
  labels: Record<string, string> | null | undefined,
  topicID: string | undefined,
): Promise<boolean> {
  const html = stopAlertHTML(runID, epicID, stop, labels);
  try {
    await sendTelegramHTML(env, html, topicID === undefined ? {} : { topic_id: topicID });
    return true;
  } catch (error) {
    console.error(
      `factory alerts: could not page run ${runID} for stop ${stop.key}: ${String(error)}`,
    );
    return false;
  }
}

/**
 * The ticks a clearing command names, as "id (label)" lines beside it — never
 * rewritten INTO the command, which must stay exactly what a person can run.
 */
function tickLegend(command: string, labels: Record<string, string> | null | undefined): string[] {
  if (labels === null || labels === undefined) return [];
  const lines: string[] = [];
  for (const word of command.split(" ")) {
    const label = labels[word];
    if (label !== undefined && label !== "" && !lines.includes(word)) {
      lines.push(word);
    }
  }
  return lines.map((id) => `${id} (${labels[id]})`);
}

/** The message a person reads: the run, the state, the reason, and the one command that clears it. */
export function stopAlertHTML(
  runID: string,
  epicID: string,
  stop: StatusStop,
  labels: Record<string, string> | null | undefined,
): string {
  const state =
    stop.kind === "person" ? "needs a person" : stop.kind === "done" ? "epic done" : "run failed";
  const lines = [`<b>${escapeHTML(runID)}</b> (epic ${escapeHTML(epicID)}) — ${state}`];
  if (stop.what !== "") lines.push(escapeHTML(stop.what));
  if (stop.clear_with !== null && stop.clear_with !== "") {
    // The command verbatim: it is the one thing in the message a person runs.
    lines.push(`clear with: <code>${escapeHTML(stop.clear_with)}</code>`);
    for (const named of tickLegend(stop.clear_with, labels)) {
      lines.push(escapeHTML(named));
    }
  }
  return lines.join("\n");
}

/**
 * The cloud run's ending, told to the operator channel from the Run Workflow's
 * finalize — the single place every cloud run's ending passes through, so
 * "epic done" and "run failed" are page-once facts even when the Workflow step
 * retries.
 *
 * Never throws: a notification that could not be delivered is recorded in the
 * stop's row and retried by nothing (there is no later push for a dead cloud
 * run) — but a notifier that could fail a finalize would be a notifier that
 * breaks the run it reports, and that order of importance is fixed here.
 */
export async function notifyRunEnded(
  env: Env,
  run: { run_id: string; project: string; epic: string },
  state: "completed" | "failed",
  detail: string,
): Promise<void> {
  try {
    const enrolment = await getEnrolledProject(env.DB, run.project).catch(() => null);
    await evaluateStatusAlerts(env, {
      run_id: run.run_id,
      doc: {
        schema_version: 1,
        run_id: run.run_id,
        epic_id: run.epic,
        host: "cloud",
        generated_at: new Date().toISOString(),
        liveness: { alive: false, state, reason: detail },
        lifecycle: { phase: state === "completed" ? "done" : "failed" },
        attention: [],
        waits_on: null,
        waves: null,
      },
      topic_id: enrolment?.telegram_topic_id,
    });
  } catch (error) {
    console.error(
      `factory alerts: could not page run ${run.run_id}'s ending (${state}): ${String(error)}`,
    );
  }
}
