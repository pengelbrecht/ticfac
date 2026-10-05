/**
 * The phone page (ticfac tick i1r): a mobile-first, read-only status view the
 * factory Worker itself serves at `/status` — the same attention-first view a
 * bare `ticfac` prints (tick 2qz), from a phone, with the laptop closed.
 *
 * ## The dashboard model, rendered (hn6, tick 0rx)
 *
 * A run card answers the terminal dashboard's questions from the SAME status
 * model, never a second opinion: how far along (a progress bar and `n/m
 * ticks` from progress, the elapsed the model states, the ETA only where
 * remaining states one), is it healthy (the verdict with its coloured dot and
 * its recovered facts), does anything need me (needs-you, quiet when nothing
 * does), then the per-tick table (plan order, children indented, one glyph per
 * pipeline stage and per try), the live workers with their activity, the CI
 * line, the honest cost line and the last two feed events. Every section is
 * conditional on the model stating its facts, so a snapshot from before hn6
 * renders exactly as it always did.
 *
 * ## One model, two hosts (hn6, tick h7w)
 *
 * Every run the page lists is answered by the 6dh status model
 * (`ticfac.status.v1`) — the run's OWN model, whichever host it lives on:
 *
 *  - a CLOUD run's orchestrator container pushes the model its own `ticfac
 *    run-epic` gathered in situ (`POST /api/status-relay`, on its run's own
 *    credential), and the page renders THAT — the same model `ticfac watch
 *    run_<hex>` renders in the terminal, verdict, table and per-source cost
 *    alike. A cloud run that has pushed nothing yet falls back to the
 *    composition the factory's own records state (cloudStatusDoc) — and a
 *    pushed model that still claims the run alive while the factory's own
 *    run row says it ended (a container that died before its terminal push)
 *    is not believed: the run row is the liveness authority for a cloud run,
 *    and the page composes the row from the records rather than render a
 *    stopped clock as a live run.
 *
 *  - a LOCAL run is the last snapshot its run pushed, and the page measures
 *    its age — because a LOCAL run pauses when the laptop sleeps, and a page
 *    that rendered a stopped clock as a live run would look stuck when the
 *    truth is "paused, and this reading is old". The stale sentence is the
 *    page's own honest word: it says plainly that a local run lives on its
 *    laptop, that a paused laptop pauses the run, and that what follows is the
 *    last reading rather than a live one.
 *
 * ## The credential never lands in a URL
 *
 * Authentication is the operator's factory credential — the same token the
 * bearer routes take, delivered as a TOKEN COOKIE: typed into a form, POSTed
 * in the request body, and set `HttpOnly` so no script on the page can read
 * it. Nothing the browser stores in history, nothing a proxy logs, nothing a
 * referrer leaks: the login POST's only redirect target is `/status` itself.
 * The Worker never holds the plaintext — it verifies the cookie exactly the
 * way it verifies the `Authorization` header, against the stored PBKDF2
 * record (`auth.ts`), on every request.
 *
 * ## Read-only, in the same sense the TUI board is
 *
 * No action on this page can steer a run: the page renders documents other
 * code wrote and takes no input beyond the login. If it ever gains actions
 * they route through the same closed command surface as the terminal commands
 * (D21), never a private path.
 *
 * ## Installable
 *
 * A PWA manifest, an icon and a pass-through service worker are served under
 * the same prefix, so the page installs to a phone's home screen as its own
 * app — the "web app I can follow from my phone" the operator asked for,
 * without an app store.
 */

import { STATUS_PAGE_PATH, verifyFactoryToken } from "./auth";
import { getRunProgress, listRuns, type Run } from "./db";
import type { Env } from "./index";
import type { PendingEntry } from "./run-room";
import { roomFor } from "./runs";
import {
  classifyStatusDoc,
  classRank,
  cloudStatusDoc,
  LOCAL_SNAPSHOT_STALE_MS,
  listStatusSnapshots,
  type StatusDoc,
  type StatusPipelineStage,
  type StatusTick,
  type StatusTry,
  type StatusWorker,
  type StoredSnapshot,
  tickRef,
  verdictWord,
} from "./status";
import { escapeHTML } from "./telegram";

/** The cookie the login sets. `HttpOnly`; the value is the operator's factory token. */
export const OPERATOR_COOKIE = "ticfac_operator";

/** How long the login lasts. Thirty days; the token outlives it or is rotated. */
const COOKIE_MAX_AGE_SECONDS = 30 * 24 * 60 * 60;

/** How many cloud runs the page lists — the listing bound `/api/runs` teaches. */
const PAGE_RUN_LIMIT = 25;

/** The page refreshes itself on this cadence: a glance, not a stream. */
const REFRESH_SECONDS = 60;

export type PageRequest = { request: Request; env: Env; url: URL };

/**
 * The page's routes: `/status` and its three install assets. Everything else
 * under the prefix is a 404, the same refusal the API gives.
 */
export async function statusPageRoute(request: Request, env: Env, url: URL): Promise<Response> {
  const subpath = url.pathname.slice(STATUS_PAGE_PATH.length);
  if (request.method === "GET" && (subpath === "" || subpath === "/")) {
    return await overviewPage(request, env);
  }
  if (subpath === "/login") {
    if (request.method !== "POST") return methodNotAllowedPage(["POST"]);
    return await loginSubmit(request, env);
  }
  if (request.method === "GET" && subpath === "/manifest.webmanifest") return manifestAsset();
  if (request.method === "GET" && subpath === "/sw.js") return serviceWorkerAsset();
  if (request.method === "GET" && subpath === "/icon.svg") return iconAsset();
  return notFoundPage();
}

// ----------------------------------------------------------------- auth ---

/** The operator credential from the cookie, or null when the request carries none. */
function cookieToken(request: Request): string | null {
  const header = request.headers.get("Cookie");
  if (header === null) return null;
  for (const part of header.split(";")) {
    const [name, ...rest] = part.trim().split("=");
    if (name === OPERATOR_COOKIE) return rest.join("=");
  }
  return null;
}

/**
 * Verifies the request's cookie against the stored token record — exactly the
 * check the bearer middleware performs, on the same secret. Returns the
 * response to send when the request must not proceed (a login page for a
 * missing or bad credential, a 503 for a misconfigured deployment), or null
 * when it is authenticated.
 */
export async function authenticatePageRequest(
  request: Request,
  env: Env,
): Promise<Response | null> {
  const stored = env.FACTORY_TOKEN_HASH;
  if (typeof stored !== "string" || stored.length === 0) {
    return htmlPage(misconfiguredLoginHTML("this factory has no credential configured"), 503);
  }
  const token = cookieToken(request);
  if (token === null || token === "") return htmlPage(loginHTML(null), 401);
  let ok = false;
  try {
    ok = await verifyFactoryToken(token, stored);
  } catch (error) {
    console.error(`factory page: the stored credential is unusable: ${String(error)}`);
    return htmlPage(misconfiguredLoginHTML("this factory's credential record is not usable"), 503);
  }
  if (!ok) return htmlPage(loginHTML("that token is not this factory's credential"), 401);
  return null;
}

async function loginSubmit(request: Request, env: Env): Promise<Response> {
  const stored = env.FACTORY_TOKEN_HASH;
  if (typeof stored !== "string" || stored.length === 0) {
    return htmlPage(misconfiguredLoginHTML("this factory has no credential configured"), 503);
  }
  // The form body, read as form data (the only thing this page's own form
  // sends). A body that is not a form at all is a bad request, never a crash.
  let token = "";
  try {
    const form = await request.formData();
    token = String(form.get("token") ?? "").trim();
  } catch {
    return badRequestForm("the login takes the factory token as a form field");
  }
  if (token === "") return htmlPage(loginHTML("type the factory token"), 401);
  let ok = false;
  try {
    ok = await verifyFactoryToken(token, stored);
  } catch (error) {
    console.error(`factory page: the stored credential is unusable: ${String(error)}`);
    return htmlPage(misconfiguredLoginHTML("this factory's credential record is not usable"), 503);
  }
  if (!ok) return htmlPage(loginHTML("that token is not this factory's credential"), 401);

  // PRG: the redirect is to the page itself, so the token — which travelled in
  // the POST body and is about to travel in a cookie — never enters a URL the
  // browser could store, share or leak. HttpOnly keeps it from scripts;
  // SameSite=Lax keeps the cookie on this origin's own navigations.
  const cookie =
    `${OPERATOR_COOKIE}=${token}; HttpOnly; Secure; SameSite=Lax; Path=/; ` +
    `Max-Age=${COOKIE_MAX_AGE_SECONDS}`;
  return new Response(null, {
    status: 303,
    headers: { Location: STATUS_PAGE_PATH, "Set-Cookie": cookie, "Cache-Control": "no-store" },
  });
}

function badRequestForm(detail: string): Response {
  return htmlPage(`<h1>ticfac factory</h1><p class="error">${escapeHTML(detail)}</p>`, 400);
}

// ------------------------------------------------------------- the page ---

/**
 * One tick row of the card's fold-out. `label` is the pre-hn6 spelling —
 * `tickRef`, the id plus the pusher's label map — and `label_text` the bare
 * label; the hn6 table prefers the model's own gloss and title.
 */
type RunRowTick = StatusTick & {
  label: string;
  label_text: string;
  /** The tick's own provenance, carried for the workers line beside the
   *  model's worker entry — the words the terminal names a worker by. */
  model: string | null;
  executor: string | null;
};

type RunRow = {
  run_id: string;
  epic_id: string;
  host: string;
  state: string;
  reason: string;
  clear_with: string | null;
  /** The model's own wave/tick listing, for the fold-out. */
  ticks: RunRowTick[];
  /** The age sentence for a LOCAL run's snapshot, or null for a live cloud row. */
  snapshot_note: string | null;
  stale: boolean;
  /**
   * The run's whole status document — every field the dashboard renders
   * reads it, never a second opinion the page would have to keep in
   * agreement. A doc without the hn6 fields renders exactly as it did
   * before they existed.
   */
  model: StatusDoc;
};

async function overviewPage(request: Request, env: Env): Promise<Response> {
  const denied = await authenticatePageRequest(request, env);
  if (denied !== null) return denied;

  const now = Date.now();
  const rows: RunRow[] = [];
  const degraded: string[] = [];

  // Every stored snapshot, read once: the local runs' own pushes and the
  // cloud runs' orchestrator pushes share one store, and the page splits
  // them by the host the envelope named.
  let snapshots: StoredSnapshot[] = [];
  try {
    snapshots = await listStatusSnapshots(env.DB);
  } catch (error) {
    console.error(`factory page: status snapshots could not be read: ${String(error)}`);
    degraded.push("runs are not listed from their snapshots: the snapshot store could not be read");
  }

  // The local runs: the last snapshot each one pushed. Staleness is measured
  // HERE, at read time, because the run that pushed it may be asleep.
  for (const snapshot of snapshots) {
    if (snapshot.host !== "local") continue;
    rows.push(localRow(snapshot, now));
  }

  // The cloud runs, keyed by their own pushed models — the run's own answer
  // wherever it has one (hn6 h7w), with each project's pending gates read
  // once per project (the room owns them, and they outlive runs).
  const pushedCloud = new Map(
    snapshots
      .filter((snapshot) => snapshot.host === "cloud")
      .map((snapshot) => [snapshot.run_id, snapshot]),
  );
  try {
    const runs = await listRuns(env.DB, { limit: PAGE_RUN_LIMIT });
    const gates = await gatesByProject(env, runs);
    for (const run of runs) {
      const progress = await getRunProgress(env.DB, run.run_id);
      // The run's own model where it has pushed one — the same one the
      // terminal renders — but never a stopped clock: the factory's run row
      // is the liveness authority for a cloud run, so a pushed model that
      // still claims the run alive after the row says it ended (a container
      // that died before its terminal push) is not believed, and the row is
      // composed from the records the factory itself holds.
      const pushed = pushedCloud.get(run.run_id) ?? null;
      const believesPush = pushed !== null && !(pushed.model.liveness.alive && runEnded(run.state));
      const doc = believesPush
        ? pushed!.model
        : cloudStatusDoc(run, gates.get(run.project) ?? [], progress, null);
      rows.push(cloudRow(doc, believesPush ? pushed! : null, now));
    }
  } catch (error) {
    console.error(`factory page: cloud runs could not be composed: ${String(error)}`);
    degraded.push("cloud runs are not listed: the run index could not be read");
  }

  // Attention first, the same band order the bare `ticfac` overview sorts by;
  // within a band the local half before the cloud half, like the terminal's
  // own "the checkout's own order before the factory's".
  rows.sort(
    (a, b) =>
      classRank(a.state) - classRank(b.state) ||
      (a.host === b.host ? 0 : a.host === "local" ? -1 : 1) ||
      a.run_id.localeCompare(b.run_id),
  );

  return htmlPage(overviewHTML(rows, degraded), 200);
}

/** The project's pending gates, read once per project the listing mentions. */
async function gatesByProject(env: Env, runs: Run[]): Promise<Map<string, PendingEntry[]>> {
  const gates = new Map<string, PendingEntry[]>();
  for (const project of [...new Set(runs.map((run) => run.project))].sort()) {
    try {
      gates.set(project, await roomFor(env, project).listQuestions());
    } catch (error) {
      console.error(`factory page: the room for ${project} could not be asked: ${String(error)}`);
      gates.set(project, []);
    }
  }
  return gates;
}

function localRow(snapshot: StoredSnapshot, now: number): RunRow {
  const classified = classifyStatusDoc(snapshot.model);
  const ageMs = now - Date.parse(snapshot.pushed_at);
  // PAUSED/STALE is a claim about a LIVE run whose pushes stopped — a laptop
  // asleep mid-run. A terminal run's old snapshot is simply its last word:
  // the age is shown, the run is not said to be paused.
  const stale =
    (Number.isNaN(ageMs) ? true : ageMs > LOCAL_SNAPSHOT_STALE_MS) && snapshot.model.liveness.alive;
  const ticks = ticksOfWaves(snapshot.model, snapshot.tick_labels);
  const ageSentence = Number.isNaN(ageMs)
    ? "the last snapshot carries no readable time"
    : `last snapshot ${formatAge(ageMs)} ago`;
  return {
    run_id: snapshot.run_id,
    epic_id: snapshot.epic_id,
    host: "local",
    state: classified.state,
    reason: classified.reason,
    clear_with: classified.clear_with,
    ticks,
    snapshot_note: stale ? `${ageSentence} — PAUSED/STALE` : ageSentence,
    stale,
    model: snapshot.model,
  };
}

// runEnded says whether the factory's own run row records the run as over.
// The row is the liveness authority for a cloud run: the Workflow itself
// writes it, and a pushed model that still claims the run alive after it is
// a stopped clock, never a live one.
function runEnded(state: string): boolean {
  return state === "completed" || state === "failed" || state === "stopped";
}

/**
 * A cloud run's row: the run's OWN model wherever its orchestrator pushed
 * one — verdict, tick table, per-source cost and all, the same model the
 * terminal's watch renders (hn6 h7w) — with the age of the last push named,
 * without the local row's PAUSED/STALE claim: a cloud run lives on the
 * factory's own container, and its run row — not the snapshot's age — is
 * what says whether it is still going. A run that pushed nothing yet keeps
 * the composed doc, and the composed doc's honest bareness.
 */
function cloudRow(doc: StatusDoc, pushed: StoredSnapshot | null, now: number): RunRow {
  const classified = classifyStatusDoc(doc);
  const labels = pushed?.tick_labels ?? null;
  const ageMs = pushed === null ? 0 : now - Date.parse(pushed.pushed_at);
  const ageNote =
    pushed === null
      ? null
      : Number.isNaN(ageMs)
        ? "the last pushed model carries no readable time"
        : `last pushed model ${formatAge(ageMs)} ago`;
  return {
    run_id: doc.run_id,
    epic_id: doc.epic_id,
    host: "cloud",
    state: classified.state,
    reason: classified.reason,
    clear_with: classified.clear_with,
    ticks: ticksOfWaves(doc, labels),
    snapshot_note: ageNote,
    stale: false,
    model: doc,
  };
}

/** The model's own wave/tick listing flattened to the fold-out's rows. */
function ticksOfWaves(model: StatusDoc, labels: Record<string, string> | null): RunRowTick[] {
  const ticks: RunRowTick[] = [];
  for (const wave of model.waves ?? []) {
    for (const tick of wave.ticks) {
      ticks.push({
        tick_id: tick.tick_id,
        label: tickRef(tick.tick_id, labels),
        label_text: labels?.[tick.tick_id] ?? "",
        state: tick.state,
        gloss: tick.gloss,
        title: tick.title,
        pipeline: tick.pipeline,
        parent_tick_id: tick.parent_tick_id,
        duration_seconds: tick.duration_seconds,
        tries: tick.tries,
        duplicate_of: tick.duplicate_of ?? null,
        model: tick.model ?? null,
        executor: tick.executor ?? null,
      });
    }
  }
  return ticks;
}

function formatAge(ms: number): string {
  if (ms < 60_000) return `${Math.max(1, Math.round(ms / 1000))}s`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m`;
  return `${Math.round(ms / 3_600_000)}h`;
}

// ---------------------------------------------------------------- HTML ---

const PAGE_STYLE = `
:root { color-scheme: dark; }
body { margin: 0; font: 16px/1.45 -apple-system, "Segoe UI", Roboto, sans-serif;
       background: #0b0e14; color: #e6e9ef; }
main { max-width: 34rem; margin: 0 auto; padding: 1rem; }
h1 { font-size: 1.1rem; margin: .4rem 0 1rem; }
.run { border: 1px solid #262c38; border-radius: .6rem; padding: .8rem 1rem;
       margin: .7rem 0; background: #11151d; }
.run.held { border-color: #b3591f; }
.run.failed { border-color: #a03030; }
.run-head { display: flex; justify-content: space-between; gap: .5rem; }
.run-id { font-weight: 700; word-break: break-all; }
.state { font-size: .8rem; padding: .1rem .5rem; border-radius: 999px;
         background: #262c38; white-space: nowrap; }
.state.held { background: #4a2c10; color: #ffb27a; }
.state.failed { background: #3d1515; color: #ff9c9c; }
.state.running { background: #11324d; color: #8fd0ff; }
.state.done, .state.cancelled { color: #9aa3b2; }
.reason { margin: .4rem 0 0; overflow-wrap: anywhere; }
.clear { margin: .4rem 0 0; font-family: ui-monospace, monospace; font-size: .85rem;
         overflow-wrap: anywhere; }
.note { color: #9aa3b2; font-size: .85rem; margin: .4rem 0 0; }
.stale { color: #ffb27a; }
.headline { margin: .5rem 0 0; }
.bar { height: .45rem; border-radius: 999px; background: #262c38; overflow: hidden; }
.bar-fill { height: 100%; background: #2563eb; border-radius: 999px; }
.headline-line { display: flex; flex-wrap: wrap; gap: .5rem; align-items: center;
                 margin: .35rem 0 0; font-size: .9rem; }
.hcount { font-weight: 600; }
.helapsed { color: #9aa3b2; }
.heta { color: #9aa3b2; }
.verdict { font-weight: 600; }
.verdict .dot { display: inline-block; width: .55rem; height: .55rem; border-radius: 50%;
                margin-right: .3rem; background: #7c8494; }
.verdict-healthy .dot { background: #4ade80; }
.verdict-degraded .dot { background: #ffb27a; }
.verdict-stopped .dot { background: #ff9c9c; }
.phases { margin: .4rem 0 0; font-size: .85rem; }
.ph { margin-right: .35rem; white-space: nowrap; }
.ph-done { color: #9aa3b2; }
.ph-active { color: #8fd0ff; font-weight: 700; }
.ph-pending { color: #7c8494; }
.needs-you { margin: .4rem 0 0; color: #ffb27a; overflow-wrap: anywhere; }
.needs-you.quiet { color: #7c8494; }
.workers { margin: .4rem 0 0; font-size: .85rem; color: #e6e9ef;
          overflow-wrap: anywhere; }
.workers.cloud, .ci { margin: .4rem 0 0; font-size: .85rem; color: #7c8494; }
.cost { margin: .4rem 0 0; font-size: .85rem; color: #9aa3b2; }
.recent { margin: .25rem 0 0; font-size: .8rem; color: #9aa3b2;
          overflow-wrap: anywhere; }
details { margin-top: .5rem; }
summary { cursor: pointer; color: #9aa3b2; font-size: .9rem; }
.ticklist { margin: .4rem 0 0; padding-left: 1rem; font-size: .9rem; }
.ticklist li { margin: .15rem 0; }
table.ticks-table { border-collapse: collapse; margin: .4rem 0 0; width: 100%;
                   font-size: .85rem; }
.ticks-table th { text-align: left; color: #7c8494; font-weight: 400; font-size: .75rem;
                  padding: .2rem .4rem .2rem 0; border-bottom: 1px solid #262c38; }
.ticks-table td { padding: .2rem .4rem .2rem 0; vertical-align: top;
                  overflow-wrap: anywhere; }
.ticks-table .c-pipeline, .ticks-table .c-attempts { white-space: nowrap;
  font-family: ui-monospace, monospace; }
.ticks-table tr.child .c-tick { padding-left: .9rem; }
/* A duplicate's row: dimmed whole, the work it belongs to named in the what
   cell — rows never move, and nothing about the row is the frontier's. */
.ticks-table tr.dup td { color: #7c8494; }
@media (max-width: 480px) {
  /* The table collapses to stacked rows: one row per tick becomes one block
     per cell, labelled by its own data-label, so a phone width reads the
     same facts the table columns carry. */
  .ticks-table thead { display: none; }
  .ticks-table, .ticks-table tbody, .ticks-table tr, .ticks-table td { display: block; }
  .ticks-table tr { border-top: 1px solid #262c38; padding: .25rem 0; }
  .ticks-table td::before { content: attr(data-label) ": "; color: #7c8494; }
  .ticks-table td.c-tick { font-weight: 700; }
  .ticks-table td.c-tick::before { content: ""; }
  .ticks-table tr.child .c-tick { padding-left: 0; }
}
footer { color: #7c8494; font-size: .8rem; margin: 1.2rem 0 2rem; }
form.login { display: flex; flex-direction: column; gap: .7rem; margin-top: 2rem; }
input { padding: .7rem; border-radius: .5rem; border: 1px solid #3a4250;
        background: #11151d; color: #e6e9ef; font-size: 1rem; }
button { padding: .7rem; border-radius: .5rem; border: 0; background: #2563eb;
         color: white; font-size: 1rem; }
.error { color: #ff9c9c; }
`;

const CSP =
  "default-src 'none'; style-src 'unsafe-inline'; script-src 'self'; img-src 'self'; " +
  "manifest-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'";

function htmlPage(body: string, status: number): Response {
  return new Response(body, {
    status,
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Content-Security-Policy": CSP,
      "Cache-Control": "no-store",
      "Referrer-Policy": "no-referrer",
    },
  });
}

function pageShell(title: string, body: string): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<meta name="theme-color" content="#0b0e14">
<meta http-equiv="refresh" content="${REFRESH_SECONDS}">
<title>${escapeHTML(title)}</title>
<link rel="manifest" href="${STATUS_PAGE_PATH}/manifest.webmanifest">
<link rel="icon" href="${STATUS_PAGE_PATH}/icon.svg" type="image/svg+xml">
<style>${PAGE_STYLE}</style>
</head>
<body>
<main>
${body}
</main>
</body>
</html>`;
}

function loginHTML(error: string | null): string {
  const errorLine = error === null ? "" : `<p class="error">${escapeHTML(error)}</p>`;
  return pageShell(
    "ticfac factory — sign in",
    `<h1>ticfac factory</h1>
<p>Sign in with this factory's token — the <code>factory_token</code> from
<code>~/.ticfacrc</code>. It is sent in the form body and kept in a cookie,
never in the URL.</p>
${errorLine}
<form class="login" method="POST" action="${STATUS_PAGE_PATH}/login">
<input type="password" name="token" autocomplete="current-password"
       placeholder="factory token" autofocus required>
<button type="submit">Sign in</button>
</form>`,
  );
}

function misconfiguredLoginHTML(detail: string): string {
  return pageShell(
    "ticfac factory — not configured",
    `<h1>ticfac factory</h1>
<p class="error">${escapeHTML(detail)}: re-run the factory deploy before signing in.</p>`,
  );
}

function overviewHTML(rows: RunRow[], degraded: string[]): string {
  const runsLine =
    rows.length === 0
      ? `<p>No runs. When a local run pushes snapshots (opted in with
<code>factory_status_push</code> in <code>~/.ticfacrc</code>) and a cloud run is
submitted to this factory, they are listed here.</p>`
      : rows.map((row) => runCardHTML(row)).join("\n");
  const degradedLines = degraded
    .map((line) => `<p class="note">${escapeHTML(line)}</p>`)
    .join("\n");
  return pageShell(
    "ticfac factory",
    `<h1>ticfac factory — every run, attention first</h1>
${runsLine}
${degradedLines}
<footer>Cloud rows render the model their orchestrator container pushed — the same one a terminal 'ticfac watch' builds; one that pushed nothing yet is composed from this factory's own records. Local rows are the last snapshot
their run pushed: a local run pauses when its laptop sleeps, and a reading older
than ${Math.round(LOCAL_SNAPSHOT_STALE_MS / 1000)}s is labelled PAUSED/STALE —
it is paused or the reading is stale, not stuck. This page takes no actions:
clear a stop with the command it names, in a terminal.</footer>`,
  );
}

function runCardHTML(row: RunRow): string {
  const stateWord =
    row.state === "held" ? "held for a person" : row.state === "running" ? "running" : row.state;
  const reason = row.reason === "" ? "" : `<p class="reason">${escapeHTML(row.reason)}</p>`;
  const clear =
    row.clear_with === null || row.clear_with === ""
      ? ""
      : `<p class="clear">clear with: ${escapeHTML(row.clear_with)}</p>`;
  const note =
    row.snapshot_note === null
      ? ""
      : `<p class="note${row.stale ? " stale" : ""}">${escapeHTML(row.snapshot_note)}</p>`;
  const dashboard = dashboardHTML(row);
  const workers = dashboard.workers;
  const ticks =
    row.ticks.length === 0
      ? ""
      : `<details><summary>ticks (${row.ticks.length})</summary>
${tickListHTML(row)}</details>`;
  return `<section class="run ${escapeHTML(row.state)}">
<div class="run-head"><span class="run-id">${escapeHTML(row.run_id)}</span>
<span class="state ${escapeHTML(row.state)}">${escapeHTML(stateWord)}</span></div>
<p class="note">${escapeHTML(row.host)} run · epic ${escapeHTML(row.epic_id)}</p>
${dashboard.headline}${dashboard.phases}${dashboard.needsYou}${reason}${clear}${note}${ticks}${workers}${dashboard.ci}${dashboard.cost}${dashboard.recent}
</section>`;
}

// ---------------------------------------------- the dashboard sections ---

/**
 * The hn6 dashboard sections of one run card, as a doc states them.
 *
 * The sections answer the same questions the terminal dashboard asks,
 * in its order — how far along (the headline's progress, elapsed and ETA),
 * is it healthy (the verdict), does anything need me (needs-you) — then the
 * table, the live workers, the CI line, the cost and the tail. Every
 * section is conditional on the model stating its facts, so a doc without
 * the hn6 fields renders every one of them as the empty string and the
 * card is exactly the card it always was:
 *
 *  - the whole dashboard block (headline, phase row, needs-you, workers,
 *    tick table, CI, recent tail) renders only for a doc that carries a
 *    health verdict — the field hn6 made required, so it is the marker
 *    that separates a post-hn6 snapshot from an older one, and an older
 *    one renders as it did before hn6 rather than half-painted;
 *  - the cost line renders whenever the doc carries cost LINES — the one
 *    hn6 field the factory's own cloud composition states — because cost is
 *    the one thing a cloud row can answer without the model's richer half.
 */
function dashboardHTML(row: RunRow): {
  headline: string;
  phases: string;
  needsYou: string;
  workers: string;
  ci: string;
  cost: string;
  recent: string;
} {
  const doc = row.model;
  const isDashboardDoc = doc.health?.verdict !== undefined;
  return {
    headline: isDashboardDoc ? headlineHTML(doc) : "",
    phases: isDashboardDoc ? phasesHTML(doc) : "",
    needsYou: isDashboardDoc ? needsYouHTML(doc) : "",
    workers: isDashboardDoc ? workersHTML(doc, row.ticks) : "",
    ci: isDashboardDoc ? ciHTML(doc) : "",
    cost: costHTML(doc),
    recent: isDashboardDoc ? recentHTML(doc) : "",
  };
}

/**
 * The headline's progress and verdict. The bar is a CSS width percentage —
 * closed over total, never a guess when the model states no counts — with
 * `n/m ticks` beside it, the elapsed and the ETA only where the model states
 * them (the elapsed is the same clamped clock the terminal header renders,
 * tick e6g), and the health verdict with its coloured dot and, where it
 * recovered things on its own, that calm fact rather than an alarm.
 *
 * The verdict's words are the shared vocabulary ([verdictWord] — the same
 * one the terminal's dashVerdict spells, pinned on both renderers by the
 * degraded and stopped goldens): a degraded summary already carries its own
 * prefix and a stopped run that said nothing reads the bare word, so no
 * headline here can double a prefix or hang a colon off nothing.
 */
function headlineHTML(doc: StatusDoc): string {
  const ticks = doc.progress?.ticks;
  const total = ticks?.total ?? 0;
  const closed = ticks?.closed ?? 0;
  const pct = total > 0 ? Math.round((closed / total) * 100) : 0;
  const count =
    ticks === undefined || ticks === null
      ? ""
      : `<span class="hcount">${escapeHTML(`${closed}/${total} ticks`)}</span>`;
  // The epic's own clock, the same field the terminal header renders — a
  // renderer that derived it here would count past a finished run, the
  // exact defect the field exists to end (tick e6g).
  const elapsedSeconds = doc.progress?.run_elapsed_seconds;
  const elapsed =
    elapsedSeconds === undefined || elapsedSeconds === null
      ? ""
      : `<span class="helapsed">${escapeHTML(humanDuration(elapsedSeconds))}</span>`;
  const remaining = doc.remaining?.approximate_seconds;
  const eta =
    remaining === undefined || remaining === null
      ? ""
      : `<span class="heta">ETA ~${escapeHTML(humanDuration(remaining))}</span>`;
  const verdict = doc.health?.verdict;
  const state = verdict?.state ?? "";
  const word = verdictWord(state, verdict?.summary ?? "");
  const recovered = (verdict?.recovered ?? [])
    .map((entry) =>
      entry.seconds === undefined || entry.seconds === null
        ? `${entry.what} ×${entry.count}`
        : `${entry.what} ${humanDuration(entry.seconds)}`,
    )
    .join(", ");
  const verdictText = recovered === "" ? word : `${word} (recovered: ${escapeHTML(recovered)})`;
  return `<div class="headline">
<div class="bar"><div class="bar-fill" style="width:${pct}%"></div></div>
<div class="headline-line">${count}${elapsed}${eta}<span class="verdict verdict-${escapeHTML(state)}"><span class="dot"></span>${escapeHTML(verdictText)}</span></div>
</div>`;
}

/**
 * The phase row: one chip per lifecycle phase the model states, each with
 * its own state glyph — ✓ done, ● active, ○ pending — in the model's own
 * order. `closeout` reads "close-out", the way every operator surface
 * spells it.
 */
function phasesHTML(doc: StatusDoc): string {
  const phases = doc.lifecycle?.phases ?? [];
  if (phases.length === 0) return "";
  const chips = phases.map((phase) => {
    const glyph = phase.state === "done" ? "✓" : phase.state === "active" ? "●" : "○";
    const name = phase.phase === "closeout" ? "close-out" : phase.phase;
    const cls =
      phase.state === "done" ? "ph-done" : phase.state === "active" ? "ph-active" : "ph-pending";
    return `<span class="ph ${cls}">${escapeHTML(name)} ${glyph}</span>`;
  });
  return `<p class="phases">${chips.join(" ")}</p>`;
}

/**
 * Needs-you: quiet when nothing needs a person — "needs you: nothing",
 * dim — and one line per thing that does, each naming the one command
 * that moves it on, where the model states one.
 */
function needsYouHTML(doc: StatusDoc): string {
  const needs = (doc.attention ?? []).filter((entry) => entry.needs_person === true);
  if (needs.length === 0) return `<p class="needs-you quiet">needs you: nothing</p>`;
  return needs
    .map((entry) => {
      const command = entry.unblock_command ?? "";
      const line =
        command === "" ? `needs you: ${entry.what}` : `needs you: ${entry.what} — ${command}`;
      return `<p class="needs-you">${escapeHTML(line)}</p>`;
    })
    .join("");
}

/**
 * The live workers, one line each — the per-agent panel the terminal
 * renders, in the same words: who the worker is (its tick, its model, its
 * executor, the handle a person finds it by), its measured activity as the
 * sparkline, its last action with its age, and the amber fact when the run
 * has nudged it as stuck. A census this machine cannot take — a cloud run's
 * workers are not on the machine that gathered the doc — is SAID, never
 * faked, in the terminal's own words; empty says the census read and
 * nothing stands, and renders nothing at all.
 */
function workersHTML(doc: StatusDoc, ticks: RunRowTick[]): string {
  const workers = doc.workers;
  if (workers === undefined || workers === null) {
    return `<p class="workers cloud">workers run in the cloud — not visible from here</p>`;
  }
  return workers
    .map((worker: StatusWorker) => {
      const tick = tickOfWorker(ticks, worker);
      const parts: string[] = [worker.tick_id];
      const model = shortModel(tick?.model ?? "");
      if (model !== "") parts.push(model);
      if (tick?.executor) parts.push(tick.executor);
      if (worker.handle) parts.push(worker.handle);
      let last = "";
      const activity = worker.activity ?? null;
      if (activity !== null) {
        const spark = sparkline(activity.buckets ?? []);
        if (spark !== "") parts.push(spark);
        if (activity.last_action) {
          last = activity.last_action;
          const age = ageSeconds(doc, activity.last_action_at ?? "");
          if (age !== null) last += ` (${humanDuration(age)} ago)`;
        }
        if ((activity.nudges ?? 0) > 0) {
          const nudged = `nudged ×${activity.nudges}`;
          last = last === "" ? nudged : `${last}  ${nudged}`;
        }
      }
      const line = last === "" ? parts.join(" · ") : `${parts.join(" · ")} · ${last}`;
      return `<p class="workers">${escapeHTML(line)}</p>`;
    })
    .join("");
}

/** The worker's own tick, matched by the attempt identity both carry —
 *  never by order, and never guessed. */
function tickOfWorker(ticks: RunRowTick[], worker: StatusWorker): RunRowTick | null {
  for (const tick of ticks) {
    if (tick.tick_id !== worker.tick_id) continue;
    if (worker.attempt !== undefined && tick.attempt !== undefined) {
      if (tick.attempt !== worker.attempt) continue;
    }
    return tick;
  }
  return null;
}

/** The model's own name, the way the terminal shortens it: the vendor
 *  prefix off, then the last path segment. */
function shortModel(model: string): string {
  let name = model;
  if (name.startsWith("cloudflare-workers-ai/")) {
    name = name.slice("cloudflare-workers-ai/".length);
  }
  const at = name.lastIndexOf("/");
  if (at >= 0 && at < name.length - 1) name = name.slice(at + 1);
  return name;
}

/** The activity window as one glyph per bucket, scaled to the window's own
 *  maximum — and a dot per bucket when nothing happened at all. The
 *  terminal's own sparkline, drawn from the same buckets. */
function sparkline(buckets: number[]): string {
  if (buckets.length === 0) return "";
  let max = 0;
  for (const value of buckets) if (value > max) max = value;
  if (max === 0) return "·".repeat(buckets.length);
  const levels = "▁▂▃▄▅▆▇█";
  let out = "";
  for (const value of buckets) {
    const idx = Math.min(
      levels.length - 1,
      Math.max(0, Math.ceil((value * levels.length) / max) - 1),
    );
    out += levels[idx];
  }
  return out;
}

/** A stamp's age against the model's own clock — never the wall's — or null
 *  when either moment is missing or will not parse. */
function ageSeconds(doc: StatusDoc, stamp: string): number | null {
  const at = Date.parse(stamp);
  const now = Date.parse(doc.generated_at);
  if (Number.isNaN(at) || Number.isNaN(now)) return null;
  const seconds = Math.round((now - at) / 1000);
  return seconds >= 0 ? seconds : null;
}

/**
 * The CI line, in the terminal's own words: the forge's checks on the epic
 * PR's head, ✓ a green check, ✗ red, ◐ with its age one still running. No
 * PR yet and the line says so, dimly — a run that has not opened its PR is
 * a fact, not a silence.
 */
function ciHTML(doc: StatusDoc): string {
  const ci = doc.ci;
  if (ci === undefined) {
    // A doc that states no ci field is a composition that never read the
    // forge — the factory's own cloud composition (hn6 5d4) — and "no PR
    // yet" is a claim only a model that LOOKED may make. The pushed model
    // always carries ci (null when it read and found no PR), so the dim
    // sentence below stays that model's answer, and the composed card
    // claims nothing about the forge at all.
    return "";
  }
  if (ci === null || ci.pr === undefined || ci.pr === null) {
    return `<p class="ci">CI: no PR yet</p>`;
  }
  const parts = (ci.checks ?? []).map((check) => {
    if (check.status === "completed" && check.conclusion === "success") {
      return `${check.name} ✓`;
    }
    if (check.status === "completed" && check.conclusion === "failure") {
      return `${check.name} ✗`;
    }
    if (check.status === "completed") {
      return `${check.name} ${check.conclusion}`;
    }
    const age = ageSeconds(doc, check.started_at);
    return age === null ? `${check.name} ◐` : `${check.name} ◐ ${humanDuration(age)}`;
  });
  const line = `CI #${ci.pr.number}${parts.length === 0 ? "" : " " + parts.join(" · ")}`;
  return `<p class="ci">${escapeHTML(line)}</p>`;
}

/**
 * The honest cost line: one segment per cost line the model states, the
 * river's own name with the measured number where one exists and "not
 * metered" where none does — never a fabricated $0.00. A doc that carries
 * a cost with no lines at all says "cost: not metered"; a doc that carries
 * no cost field says nothing (that is the pre-hn6 doc's honest answer).
 */
function costHTML(doc: StatusDoc): string {
  const lines = doc.cost?.lines;
  if (!Array.isArray(lines)) return "";
  if (lines.length === 0) return `<p class="cost">cost: not metered</p>`;
  const segments = lines.map((line) => {
    const amount =
      line.usd === undefined || line.usd === null || line.metered === false
        ? "not metered"
        : `$${line.usd.toFixed(2)}`;
    return `${escapeHTML(costSourceName(line.source))} ${escapeHTML(amount)}`;
  });
  return `<p class="cost">cost: ${segments.join(" · ")}</p>`;
}

/** The cost vocabulary's own names, the way the cost row reads them. */
function costSourceName(source: string): string {
  switch (source) {
    case "workers-ai":
      return "Workers AI";
    case "pi-local":
      return "pi (local)";
    default:
      return source;
  }
}

/**
 * The tail: the model's own last words, the last two feed events — the tail
 * a dashboard shows where a watcher once drowned in the whole stream. The
 * clock is the event's own stamp; a tick the event does not name is the
 * run itself.
 */
function recentHTML(doc: StatusDoc): string {
  const events = doc.recent ?? [];
  return events
    .slice(-2)
    .map((event) => {
      const clock = event.at.slice(11, 16);
      const who = event.tick_id === undefined || event.tick_id === null ? "run" : event.tick_id;
      const line = `${clock} ${who} ${event.stage}: ${event.detail}`;
      return `<p class="recent">${escapeHTML(line)}</p>`;
    })
    .join("");
}

/**
 * The fold-out's tick listing. A doc with the hn6 fields gets the table —
 * one row per tick in the tracker's own plan order, children indented under
 * the parent the model names, the pipeline cell with the same glyphs the
 * terminal draws (✓ done, ● active, ✗ failed, one … for the pending tail
 * behind the live stage), the tick's measured time, and one glyph per try
 * so a refusal is history a person can see, not an alarm. An older doc
 * keeps the list it always had, so old snapshots render as before.
 */
function tickListHTML(row: RunRow): string {
  if (row.model.health?.verdict === undefined) {
    return `<ul class="ticklist">${row.ticks
      .map((tick) => `<li>${escapeHTML(tick.label)} — ${escapeHTML(tick.state)}</li>`)
      .join("")}</ul>`;
  }
  const body = row.ticks
    .map((tick) => {
      const child = tick.parent_tick_id !== undefined && tick.parent_tick_id !== null;
      const dup = tick.duplicate_of !== undefined && tick.duplicate_of !== null;
      const id = child ? `└ ${tick.tick_id}` : tick.tick_id;
      // A duplicate is not work the epic owes: its row keeps its place and
      // its history, dimmed, with the tick the work belongs to in the what
      // cell — the same words the terminal's dashboard renders (hn6 gmo).
      const what = dup
        ? `duplicate of ${tick.duplicate_of}`
        : (tick.gloss ?? tick.title ?? tick.label_text);
      return (
        `<tr class="trow${child ? " child" : ""}${dup ? " dup" : ""}">` +
        `<td class="c-tick">${escapeHTML(id)}</td>` +
        `<td class="c-what" data-label="what">${escapeHTML(what)}</td>` +
        `<td class="c-pipeline" data-label="pipeline">${escapeHTML(pipelineGlyphs(tick.pipeline))}</td>` +
        `<td class="c-time" data-label="time">${escapeHTML(
          tick.duration_seconds === undefined || tick.duration_seconds === null
            ? ""
            : humanDuration(tick.duration_seconds),
        )}</td>` +
        `<td class="c-attempts" data-label="attempts">${escapeHTML(tryGlyphs(tick.tries))}</td>` +
        "</tr>"
      );
    })
    .join("");
  return `<table class="ticks-table">
<thead><tr><th>tick</th><th>what</th><th>pipeline</th><th>time</th><th>attempts</th></tr></thead>
<tbody>${body}</tbody>
</table>`;
}

/**
 * The pipeline cell's glyphs: ✓ per done stage, the live stage's own glyph
 * (● active, ✗ failed) where it stands, and ONE … for every pending stage
 * behind it — the cell states where a tick IS, and the stages nobody
 * reached are a tail, not a row of circles. A cell with no stages at all
 * is a single …: a cell nobody filled says nothing stronger than that.
 */
function pipelineGlyphs(stages: StatusPipelineStage[] | undefined): string {
  if (stages === undefined || stages.length === 0) return "…";
  const glyphs: string[] = [];
  for (let i = 0; i < stages.length; i++) {
    const state = stages[i]?.state;
    if (state === "done") {
      glyphs.push("✓");
      continue;
    }
    if (state === "active" || state === "failed") {
      glyphs.push(state === "active" ? "●" : "✗");
      if (i < stages.length - 1) glyphs.push("…");
    } else {
      glyphs.push("…");
    }
    break;
  }
  return glyphs.join(" ");
}

/**
 * One glyph per try, in try order, so a refusal is history a person can
 * see: ✓ closed, ✗ rejected or gate-refused, ● in flight, ○ anything the
 * records have out but not judged.
 */
function tryGlyphs(tries: StatusTry[] | undefined): string {
  return (tries ?? [])
    .map((try_) => {
      switch (try_.outcome) {
        case "closed":
          return "✓";
        case "rejected":
        case "gate-failed":
          return "✗";
        case "in-flight":
          return "●";
        default:
          return "○";
      }
    })
    .join("");
}

/**
 * A person's clock for the card's timers — the same grades the terminal
 * renders: seconds under a minute, minutes under an hour, hours and minutes
 * under a day, days and hours past that. Rounded for glancing, because the
 * exact numbers live in the model.
 */
function humanDuration(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86_400) {
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor(seconds / 60) % 60;
    return minutes === 0 ? `${hours}h` : `${hours}h${minutes}m`;
  }
  const days = Math.floor(seconds / 86_400);
  return `${days}d${Math.floor((seconds % 86_400) / 3600)}h`;
}

// ----------------------------------------------------------- PWA assets ---

function asset(body: string, contentType: string, cacheControl: string): Response {
  return new Response(body, {
    headers: { "Content-Type": contentType, "Cache-Control": cacheControl },
  });
}

function manifestAsset(): Response {
  const manifest = {
    name: "ticfac factory",
    short_name: "ticfac",
    description: "every run, attention first — from a phone",
    start_url: STATUS_PAGE_PATH,
    display: "standalone",
    background_color: "#0b0e14",
    theme_color: "#0b0e14",
    icons: [{ src: `${STATUS_PAGE_PATH}/icon.svg`, sizes: "any", type: "image/svg+xml" }],
  };
  return asset(JSON.stringify(manifest), "application/manifest+json", "public, max-age=86400");
}

/**
 * A pass-through service worker. It exists to make the page installable, and
 * deliberately caches nothing: this page's whole value is being CURRENT, and a
 * cached status view is exactly the stopped clock this page refuses to render.
 */
function serviceWorkerAsset(): Response {
  const source = `// Pass-through service worker (ticfac tick i1r): installability only.
// No fetch interception, no cache — the page must always read fresh.
self.addEventListener("fetch", () => {});
`;
  return asset(source, "application/javascript", "public, max-age=86400");
}

function iconAsset(): Response {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
<rect width="512" height="512" rx="96" fill="#0b0e14"/>
<circle cx="256" cy="144" r="40" fill="#2563eb"/>
<rect x="128" y="232" width="256" height="36" rx="18" fill="#3a4250"/>
<rect x="128" y="300" width="256" height="36" rx="18" fill="#262c38"/>
<rect x="128" y="368" width="168" height="36" rx="18" fill="#b3591f"/>
</svg>`;
  return asset(svg, "image/svg+xml", "public, max-age=86400");
}

function methodNotAllowedPage(allow: string[]): Response {
  return htmlPage(`method not allowed (${allow.join(", ")})`, 405);
}

function notFoundPage(): Response {
  return htmlPage("not found", 404);
}
