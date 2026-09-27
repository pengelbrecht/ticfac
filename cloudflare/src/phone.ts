/**
 * The phone page (ticfac tick i1r): a mobile-first, read-only status view the
 * factory Worker itself serves at `/status` — the same attention-first view a
 * bare `ticfac` prints (tick 2qz), from a phone, with the laptop closed.
 *
 * ## One model, two hosts
 *
 * Every run the page lists is answered by the 6dh status model
 * (`ticfac.status.v1`):
 *
 *  - cloud runs are composed NATIVELY at read time (`status.ts`), so they are
 *    live — the factory's own records are the source;
 *  - local runs are the last snapshot their run pushed, and the page measures
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
  type StoredSnapshot,
  tickRef,
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

type RunRow = {
  run_id: string;
  epic_id: string;
  host: string;
  state: string;
  reason: string;
  clear_with: string | null;
  /** The model's own wave/tick listing, for the fold-out. */
  ticks: { tick_id: string; label: string; state: string }[];
  /** The age sentence for a LOCAL run's snapshot, or null for a live cloud row. */
  snapshot_note: string | null;
  stale: boolean;
};

async function overviewPage(request: Request, env: Env): Promise<Response> {
  const denied = await authenticatePageRequest(request, env);
  if (denied !== null) return denied;

  const now = Date.now();
  const rows: RunRow[] = [];
  const degraded: string[] = [];

  // The local runs: the last snapshot each one pushed. Staleness is measured
  // HERE, at read time, because the run that pushed it may be asleep.
  try {
    for (const snapshot of await listStatusSnapshots(env.DB)) {
      rows.push(localRow(snapshot, now));
    }
  } catch (error) {
    console.error(`factory page: local snapshots could not be read: ${String(error)}`);
    degraded.push("local runs are not listed: the snapshot store could not be read");
  }

  // The cloud runs: composed natively from the factory's own records — the
  // listing the `/api/runs` table teaches, with each project's pending gates
  // read once per project (the room owns them, and they outlive runs).
  try {
    const runs = await listRuns(env.DB, { limit: PAGE_RUN_LIMIT });
    const gates = await gatesByProject(env, runs);
    for (const run of runs) {
      const progress = await getRunProgress(env.DB, run.run_id);
      const doc = cloudStatusDoc(run, gates.get(run.project) ?? [], progress, null);
      rows.push(cloudRow(doc));
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
  const ticks: RunRow["ticks"] = [];
  for (const wave of snapshot.model.waves ?? []) {
    for (const tick of wave.ticks) {
      ticks.push({
        tick_id: tick.tick_id,
        label: tickRef(tick.tick_id, snapshot.tick_labels),
        state: tick.state,
      });
    }
  }
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
  };
}

function cloudRow(doc: ReturnType<typeof cloudStatusDoc>): RunRow {
  const classified = classifyStatusDoc(doc);
  return {
    run_id: doc.run_id,
    epic_id: doc.epic_id,
    host: "cloud",
    state: classified.state,
    reason: classified.reason,
    clear_with: classified.clear_with,
    ticks: [],
    snapshot_note: null,
    stale: false,
  };
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
details { margin-top: .5rem; }
summary { cursor: pointer; color: #9aa3b2; font-size: .9rem; }
.ticklist { margin: .4rem 0 0; padding-left: 1rem; font-size: .9rem; }
.ticklist li { margin: .15rem 0; }
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
<footer>Cloud rows are live from this factory. Local rows are the last snapshot
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
  const ticks =
    row.ticks.length === 0
      ? ""
      : `<details><summary>ticks (${row.ticks.length})</summary>
<ul class="ticklist">${row.ticks
          .map((tick) => `<li>${escapeHTML(tick.label)} — ${escapeHTML(tick.state)}</li>`)
          .join("")}</ul></details>`;
  return `<section class="run ${escapeHTML(row.state)}">
<div class="run-head"><span class="run-id">${escapeHTML(row.run_id)}</span>
<span class="state ${escapeHTML(row.state)}">${escapeHTML(stateWord)}</span></div>
<p class="note">${escapeHTML(row.host)} run · epic ${escapeHTML(row.epic_id)}</p>
${reason}${clear}${note}${ticks}
</section>`;
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
