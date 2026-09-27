import { env, SELF } from "cloudflare:test";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { insertRun } from "../src/db";

/**
 * The phone page's auth and listing (tick i1r): `/status` is the page an
 * operator follows from a phone with the laptop closed, and these are the two
 * properties the acceptance names —
 *
 *  - the page NEEDS the operator's factory credential, and the credential
 *    NEVER lands in a URL (no token in a Location header, in the login form's
 *    markup, or anywhere the browser would store or share it);
 *  - it lists cloud runs live and local runs from their last snapshot, the
 *    local rows marked PAUSED/STALE by the snapshot's age — saying plainly
 *    that a local run pauses when its laptop sleeps, rather than looking
 *    stuck.
 */

const BASE = "https://factory.example.com";

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

afterEach(async () => {
  await env.DB.prepare("DELETE FROM status_snapshots").run();
  await env.DB.prepare("DELETE FROM status_alerts").run();
  await env.DB.prepare("DELETE FROM runs").run();
});

const auth = (): Record<string, string> => ({ Authorization: `Bearer ${token}` });

async function pushSnapshot(
  runID: string,
  doc: Record<string, unknown>,
  labels?: Record<string, string>,
): Promise<void> {
  const res = await SELF.fetch(`${BASE}/api/status-snapshots`, {
    method: "POST",
    headers: auth(),
    body: JSON.stringify({
      schema_version: 1,
      run_id: runID,
      host: "local",
      pushed_at: new Date().toISOString(),
      model: doc,
      ...(labels === undefined ? {} : { tick_labels: labels }),
    }),
  });
  expect(res.status).toBe(201);
}

/** A `ticfac.status.v1` document as the Go side emits it. */
function localDoc(runID: string, epicID: string, extra: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    run_id: runID,
    epic_id: epicID,
    host: "local",
    generated_at: new Date().toISOString(),
    degraded: [],
    liveness: { alive: true, state: "alive", reason: "", source: "run.pid" },
    lifecycle: { phase: "waves", phases: [], wave: { active: 1, total: 2 } },
    progress: { ticks: { total: 4, closed: 2, open: 2 }, waves: { total: 2, done: 1, active: 1 } },
    waves: [
      {
        wave: 1,
        state: "active",
        ticks: [
          { tick_id: "i1r", title: "Follow ticfac from a phone", state: "in-flight" },
          { tick_id: "w9b", title: "The tick whose label this page names", state: "closed" },
        ],
      },
    ],
    workers: null,
    waits_on: { kind: "workers", what: "2 in-flight attempt(s)", needs_person: false },
    attention: [],
    health: { remote_retries: 0, interventions: 0, stall_warnings: 0, wall_clocks_fired: 0 },
    gates: [],
    ci: null,
    cost: { recorded_usd: 0, attempts: 3, basis: "model exchanges only" },
    remaining: null,
    ...extra,
  };
}

async function cloudRunRow(
  runID: string,
  state: "running" | "failed" | "completed",
): Promise<void> {
  await insertRun(env.DB, {
    run_id: runID,
    project: "ticks-test/phone-page",
    epic: "ko8",
    base_sha: "3e15bff81cd888e82dfe521c507a46f4ddf6913b",
    requested_by: "operator@example.com",
    state,
    started_at: new Date().toISOString(),
    ended_at: state === "running" ? null : new Date().toISOString(),
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  });
}

/** Signs in through the form and returns the cookie the page then takes. */
async function login(): Promise<string> {
  const res = await SELF.fetch(`${BASE}/status/login`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ token }).toString(),
    // Manual: a real browser follows the 303 keeping the Set-Cookie; a test
    // client that follows it would land on the page WITHOUT the cookie and
    // read the login page's 401 as the login's answer.
    redirect: "manual",
  });
  expect(res.status).toBe(303);
  return res.headers.get("Set-Cookie")!;
}

async function page(cookie?: string): Promise<Response> {
  return SELF.fetch(`${BASE}/status`, {
    headers: cookie === undefined ? {} : { Cookie: cookie },
  });
}

describe("the page needs the operator's credential, never in a URL", () => {
  it("answers a login page, not the listing, without a session", async () => {
    const res = await page();
    expect(res.status).toBe(401);
    expect(res.headers.get("Content-Type")).toContain("text/html");
    const body = await res.text();
    expect(body).toContain("/status/login");
    expect(body).toContain("factory token");
    // The credential never lands anywhere a URL could carry it — not in this
    // page, and most of all not in a query string the browser would store.
    expect(body).not.toContain(token);
    expect(body).not.toContain(`token=${token}`);
  });

  it("refuses a wrong token without setting a session", async () => {
    const res = await SELF.fetch(`${BASE}/status/login`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ token: mintFactoryToken() }).toString(),
    });
    expect(res.status).toBe(401);
    expect(res.headers.get("Set-Cookie")).toBeNull();
  });

  it("sets an HttpOnly, Secure, SameSite cookie and redirects to the page, not to a URL carrying the token", async () => {
    const res = await SELF.fetch(`${BASE}/status/login`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ token }).toString(),
      redirect: "manual",
    });
    expect(res.status).toBe(303);
    // The only redirect target is the page itself: nothing the browser keeps
    // in history, nothing a proxy logs, nothing a referrer leaks.
    expect(res.headers.get("Location")).toBe("/status");
    const cookie = res.headers.get("Set-Cookie")!;
    expect(cookie).toContain("HttpOnly");
    expect(cookie).toContain("Secure");
    expect(cookie).toContain("SameSite=Lax");
    expect(cookie).toContain("ticfac_operator=");
    expect(cookie).not.toContain(`Location=${token}`);
  });

  it("refuses a cookie carrying a wrong token", async () => {
    const res = await page(`ticfac_operator=${mintFactoryToken()}`);
    expect(res.status).toBe(401);
    const body = await res.text();
    expect(body).toContain("that token is not this factory's credential");
    expect(body).not.toContain(token);
  });

  it("serves the listing to a valid session cookie", async () => {
    const res = await page(await login());
    expect(res.status).toBe(200);
    const body = await res.text();
    expect(body).toContain("every run, attention first");
  });
});

describe("the listing: cloud live, local from its last snapshot", () => {
  it("lists a local run with its reason and names its ticks 'id (label)'", async () => {
    await pushSnapshot("epic-2jn", localDoc("epic-2jn", "2jn"), {
      i1r: "Follow ticfac from a phone",
      w9b: "The tick whose label this page names",
    });
    const body = await (await page(await login())).text();
    expect(body).toContain("epic-2jn");
    expect(body).toContain("running");
    expect(body).toContain("2 in-flight attempt(s)");
    expect(body).toContain("i1r (Follow ticfac from a phone)");
    expect(body).toContain("w9b (The tick whose label this page names)");
  });

  it("does not call a finished run's last word paused — PAUSED/STALE is a live-run claim", async () => {
    await pushSnapshot(
      "epic-done",
      localDoc("epic-done", "done", {
        liveness: {
          alive: false,
          state: "completed",
          reason: "the close-out finished",
          source: "run.pid",
        },
        lifecycle: { phase: "done", phases: [], wave: null },
        waits_on: null,
      }),
    );
    const old = new Date(Date.now() - 10 * 60_000).toISOString();
    await env.DB.prepare("UPDATE status_snapshots SET pushed_at = ? WHERE run_id = ?")
      .bind(old, "epic-done")
      .run();

    const body = await (await page(await login())).text();
    expect(body).toContain("epic-done");
    expect(body).toContain("done");
    // The age is shown; the run is not said to be paused — it ended. (The
    // footer still explains the PAUSED/STALE rule; the run's own note must
    // not carry the label.)
    expect(body).not.toContain("— PAUSED/STALE");
    expect(body).toContain("last snapshot");
  });

  it("marks a LIVE local run's old snapshot PAUSED/STALE and says why, plainly", async () => {
    await pushSnapshot("epic-2jn", localDoc("epic-2jn", "2jn"));
    // The push above is current; age it as a sleeping laptop would.
    const old = new Date(Date.now() - 10 * 60_000).toISOString();
    await env.DB.prepare("UPDATE status_snapshots SET pushed_at = ? WHERE run_id = ?")
      .bind(old, "epic-2jn")
      .run();

    const body = await (await page(await login())).text();
    expect(body).toContain("PAUSED/STALE");
    // The plain sentence the tick demands: a paused laptop is not a stuck run.
    expect(body).toContain("a local run pauses when its laptop sleeps");
  });

  it("lists a held run first, with the reason and the ONE command that clears it", async () => {
    await cloudRunRow("run_live", "running");
    await pushSnapshot(
      "epic-2jn",
      localDoc("epic-2jn", "2jn", {
        liveness: { alive: false, state: "held", reason: "", source: "run.pid" },
        waits_on: null,
        attention: [
          {
            kind: "held-for-person",
            what: "attempt 9 of tick w9b is held for a person: blocked",
            since: new Date().toISOString(),
            needs_person: true,
            unblock_command: 'ticfac settle 2jn w9b 9 --release "who"',
          },
        ],
      }),
      { w9b: "The tick whose label this page names" },
    );

    const body = await (await page(await login())).text();
    expect(body).toContain("held for a person");
    expect(body).toContain("attempt 9 of tick w9b is held for a person: blocked");
    expect(body).toContain("clear with: ticfac settle 2jn w9b 9");
    // Attention first: the held run's card stands before the running one's.
    expect(body.indexOf("epic-2jn")).toBeLessThan(body.indexOf("run_live"));
  });

  it("lists a cloud run live, and a failed one with the resume that clears it", async () => {
    await cloudRunRow("run_ok", "running");
    await cloudRunRow("run_bad", "failed");
    const body = await (await page(await login())).text();
    expect(body).toContain("run_ok");
    expect(body).toContain("cloud run · epic ko8");
    expect(body).toContain("run_bad");
    expect(body).toContain("failed");
    expect(body).toContain("clear with: ticfac run-epic ko8");
    // Attention first even between cloud rows: failed before running.
    expect(body.indexOf("run_bad")).toBeLessThan(body.indexOf("run_ok"));
  });
});

describe("the page is installable to a phone home screen", () => {
  it("serves a PWA manifest pointing at the page", async () => {
    const res = await SELF.fetch(`${BASE}/status/manifest.webmanifest`);
    expect(res.status).toBe(200);
    expect(res.headers.get("Content-Type")).toContain("application/manifest+json");
    const manifest = (await res.json()) as { start_url: string; display: string; icons: unknown[] };
    expect(manifest.start_url).toBe("/status");
    expect(manifest.display).toBe("standalone");
    expect(manifest.icons.length).toBeGreaterThan(0);
  });

  it("serves a pass-through service worker and an icon", async () => {
    const sw = await SELF.fetch(`${BASE}/status/sw.js`);
    expect(sw.status).toBe(200);
    expect(sw.headers.get("Content-Type")).toContain("application/javascript");
    // No fetch interception: a cached status view is the stopped clock the
    // page refuses to render.
    const source = await sw.text();
    expect(source).toContain('self.addEventListener("fetch", () => {})');

    const icon = await SELF.fetch(`${BASE}/status/icon.svg`);
    expect(icon.status).toBe(200);
    expect(icon.headers.get("Content-Type")).toContain("image/svg+xml");
  });

  it("refuses an unknown sub-path", async () => {
    const res = await SELF.fetch(`${BASE}/status/not-a-thing`);
    expect(res.status).toBe(404);
  });
});
