/**
 * Reading tick records from a Worker with no checkout (tick kya's reader,
 * kept: the sweep frontier, the signal funnel, the TS reconciler's tracker
 * client and the control plane's own tracker writer all stand on it).
 *
 * The tracker lives in git and the control plane has no checkout — but it does
 * not need one: `.tick/issues/<id>.json` is a TRACKED file, and this Worker
 * already reads tracked files at a commit through GitHub's contents API —
 * `.tick/runners.toml` in repo-config.ts, a worker's `RESULT-<tick>.md` in
 * worker-collect.ts. A tick record names its `parent`, so the reader here can
 * walk a membership chain exactly as `cloudIsDescendant` does in Go.
 *
 * (The wave door that first consumed the reader is deleted — tick l6t —
 * together with its `checkWaveMembership`; the reader is not part of that
 * deletion because it has the other consumers above.)
 *
 * What keeps this from decaying into a reader that quietly reads nothing is
 * `contracts/tracker-layout.json`: the path and field names below are
 * pinned to Go's `internal/tick` store from both suites, so a tracker layout
 * change fails a test rather than turning silently into nulls.
 */

import type { Env } from "./index";
import { GITHUB_API_BASE_URL } from "./progress";

// --------------------------------------------------------------- the layout ---

/**
 * Where Go's `tick.Store` keeps one tick per file (`internal/tick/store.go`,
 * `issuesDir`/`path`), and the three fields this module reads out of one.
 *
 * Pinned by `contracts/tracker-layout.json` from both languages. See the
 * header: an unreadable tracker is an allowed wave, so a silent layout drift
 * would not surface as an error here — it would surface as a check that never
 * refuses anything again.
 */
export const TICK_RECORD_DIR = ".tick/issues";

/** The tracked path of one tick's record. */
export function tickRecordPath(tickID: string): string {
  return `${TICK_RECORD_DIR}/${tickID}.json`;
}

/** The most of one tick record this reader will read. A tick is prose, not a payload. */
export const MAX_TICK_RECORD_BYTES = 256_000;

/** What this module needs out of a tick record. Everything else is the container's business. */
export type TickRecord = {
  id: string;
  type: string;
  /** Empty when the tick has no parent — Go omits the field entirely. */
  parent: string;
  /**
   * The `<source>:<ref>` a signal was filed from, when one was.
   *
   * Not read by the membership walk. It is here because the funnel's own
   * reconciler (`signal-inbox.ts`) asks this same reader whether a specific
   * tick record is the one IT committed, and the external ref is the only
   * field that answers that — a candidate id may have been taken by anybody.
   * One parser of Go's format rather than two is the point; see
   * `.tick/learnings.md` on formats read from both languages.
   *
   * Empty for a tick no signal produced, exactly as Go omits it.
   */
  external_ref: string;
};

/** Go's `tick.TypeEpic`. */
export const EPIC_TYPE = "epic";

/**
 * Parses one tick record, or null when it is not one.
 *
 * Deliberately tolerant about everything it does not use: this is a reader of
 * someone else's format, and a new field in Go must never make a wave
 * unanswerable here.
 */
export function parseTickRecord(text: string): TickRecord | null {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return null;
  }
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return null;
  const record = raw as Record<string, unknown>;
  if (typeof record.id !== "string" || record.id === "") return null;
  const type = typeof record.type === "string" ? record.type : "";
  const parent = typeof record.parent === "string" ? record.parent : "";
  const externalRef = typeof record.external_ref === "string" ? record.external_ref : "";
  return { id: record.id, type, parent, external_ref: externalRef };
}

// ----------------------------------------------------------------- the seam ---

/**
 * A reader of one tick's tracked record at one commit.
 *
 * A seam for the same reason `RepoConfigReader` and `WorkerCollector` are: the
 * rule worth testing is what the dispatch door does with a verdict, and a rule
 * exercisable only against a real GitHub repository is a rule nobody tests. A
 * deployment gets the GitHub reader below; a test assigns its own to
 * `env.TICK_TRACKER`.
 *
 * `null` means the tracker has no such tick at that commit — an answer.
 * Throwing means the record could not be read at all — not an answer, and the
 * two must stay distinct all the way to the verdict.
 */
export interface TrackerReader {
  read(project: string, ref: string, tickID: string): Promise<string | null>;
}

/**
 * GitHub's contents API, asked for one tick record at one commit.
 *
 * Mirrors `githubRepoConfig`, down to the raw accept header and the user agent
 * GitHub requires: a 404 is an answer (no such tick), any other non-2xx is
 * not.
 */
export function githubTrackerReader(env: Env): TrackerReader {
  const base = (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
  const headers: Record<string, string> = {
    accept: "application/vnd.github.raw",
    "user-agent": "ticks-factory",
  };
  const token = env.GITHUB_TOKEN;
  if (typeof token === "string" && token.trim() !== "") {
    headers.authorization = `Bearer ${token.trim()}`;
  }

  return {
    async read(project: string, ref: string, tickID: string): Promise<string | null> {
      const path = tickRecordPath(tickID);
      // An empty ref means "the repository's default branch", which is what
      // the contents API does with no `?ref` at all — and emphatically not
      // `?ref=`, which names a branch called "". Every membership check passes
      // a real commit; the funnel's reconciler is the caller that has only a
      // signal's optional branch, and for most signals that is unset.
      const query = ref === "" ? "" : `?ref=${encodeURIComponent(ref)}`;
      const url = `${base}/repos/${project}/contents/${path}${query}`;
      const response = await fetch(url, { headers });
      if (response.status === 404) return null;
      if (!response.ok) {
        throw new Error(`GitHub answered HTTP ${response.status} reading ${path}@${ref}`);
      }
      const text = await response.text();
      if (text.length > MAX_TICK_RECORD_BYTES) {
        throw new Error(
          `${path}@${ref} is ${text.length} bytes, past the ${MAX_TICK_RECORD_BYTES} this reader will read`,
        );
      }
      return text;
    },
  };
}

/** The reader this deployment uses: a test's fake, or GitHub. */
export function trackerReader(env: Env): TrackerReader {
  const injected = env.TICK_TRACKER;
  return injected === undefined || injected === null ? githubTrackerReader(env) : injected;
}
