import { env, SELF } from "cloudflare:test";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { evaluateStatusAlerts, stopAlertHTML, stopsFromStatusDoc } from "../src/notify";
import type { StatusDoc } from "../src/status";
import { classifyStatusDoc } from "../src/status";

/**
 * The notification half of tick i1r: a run needing a person produces ONE
 * Telegram message naming the reason and the clearing command — deduplicated
 * PER STOP, not per push, rate-limited, and re-armed when a stop clears and
 * comes back.
 *
 * The Bot API is faked on the global fetch (the same stand-in the Telegram
 * route tests use), so what is proven is the factory's own decision to send —
 * the message's shape, its once-ness, its naming of ticks as "id (label)" —
 * against the real D1 dedup table.
 */

const BASE = "https://factory.example.com";

let token: string;
const originalHash = env.FACTORY_TOKEN_HASH;
const previousTelegram = {
  botToken: env.TELEGRAM_BOT_TOKEN,
  user: env.TELEGRAM_USER_ID,
  chat: env.TELEGRAM_CHAT_ID,
  api: env.TELEGRAM_API_BASE_URL,
};

type BotCall = { url: string; body: Record<string, unknown> };

let calls: BotCall[];
let originalFetch: typeof fetch;

beforeAll(async () => {
  token = mintFactoryToken();
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
});

afterAll(() => {
  if (originalHash === undefined) delete env.FACTORY_TOKEN_HASH;
  else env.FACTORY_TOKEN_HASH = originalHash;
  if (previousTelegram.botToken === undefined) delete env.TELEGRAM_BOT_TOKEN;
  else env.TELEGRAM_BOT_TOKEN = previousTelegram.botToken;
  if (previousTelegram.user === undefined) delete env.TELEGRAM_USER_ID;
  else env.TELEGRAM_USER_ID = previousTelegram.user;
  if (previousTelegram.chat === undefined) delete env.TELEGRAM_CHAT_ID;
  else env.TELEGRAM_CHAT_ID = previousTelegram.chat;
  if (previousTelegram.api === undefined) delete env.TELEGRAM_API_BASE_URL;
  else env.TELEGRAM_API_BASE_URL = previousTelegram.api;
});

beforeEach(() => {
  // The operator channel: the same env `telegramConfig` reads, pointed at the
  // fake Bot API.
  env.TELEGRAM_BOT_TOKEN = "test-bot-token";
  env.TELEGRAM_USER_ID = "424242";
  env.TELEGRAM_CHAT_ID = "-1001919191";
  env.TELEGRAM_API_BASE_URL = BASE;

  calls = [];
  originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({
      url: String(input),
      body: JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>,
    });
    return new Response(JSON.stringify({ ok: true, result: { message_id: calls.length } }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
});

afterEach(async () => {
  globalThis.fetch = originalFetch;
  await env.DB.prepare("DELETE FROM status_snapshots").run();
  await env.DB.prepare("DELETE FROM status_alerts").run();
});

const auth = (): Record<string, string> => ({ Authorization: `Bearer ${token}` });

/** A `ticfac.status.v1` document as the Go side emits it. */
function doc(
  runID: string,
  epicID: string,
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    schema_version: 1,
    run_id: runID,
    epic_id: epicID,
    host: "local",
    generated_at: new Date().toISOString(),
    degraded: [],
    liveness: { alive: true, state: "alive", reason: "", source: "run.pid" },
    lifecycle: { phase: "waves", phases: [], wave: { active: 1, total: 2 } },
    progress: null,
    waves: null,
    workers: null,
    waits_on: { kind: "workers", what: "1 in-flight attempt(s)", needs_person: false },
    attention: [],
    health: { remote_retries: 0, interventions: 0, stall_warnings: 0, wall_clocks_fired: 0 },
    gates: [],
    ci: null,
    cost: { recorded_usd: 0, attempts: 1, basis: "model exchanges only" },
    remaining: null,
    ...extra,
  };
}

function heldDoc(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return doc("epic-2jn", "2jn", {
    liveness: { alive: false, state: "held", reason: "", source: "run.pid" },
    waits_on: null,
    attention: [
      {
        kind: "held-for-person",
        what: "attempt 13 of tick i1r is held for a person: blocked",
        since: new Date().toISOString(),
        needs_person: true,
        unblock_command: 'ticfac settle 2jn i1r 13 --release "who"',
      },
    ],
    ...extra,
  });
}

/** Pushes a snapshot through the real door, which is what drives the evaluator. */
async function push(
  runID: string,
  model: Record<string, unknown>,
  labels?: Record<string, string>,
) {
  const res = await SELF.fetch(`${BASE}/api/status-snapshots`, {
    method: "POST",
    headers: auth(),
    body: JSON.stringify({
      schema_version: 1,
      run_id: runID,
      host: "local",
      pushed_at: new Date().toISOString(),
      model,
      ...(labels === undefined ? {} : { tick_labels: labels }),
    }),
  });
  expect(res.status).toBe(201);
  return (await res.json()) as { alerts: { sent: number; pending: number; cleared: number } };
}

const sentTexts = (): string[] =>
  calls.filter((call) => call.url.includes("/sendMessage")).map((call) => String(call.body.text));

/** Ages every notified row out of the rate-limit window, without waiting. */
async function ageAlertsPastRateLimit(): Promise<void> {
  const old = new Date(Date.now() - 120_000).toISOString();
  await env.DB.prepare("UPDATE status_alerts SET notified_at = ? WHERE notified_at IS NOT NULL")
    .bind(old)
    .run();
}

describe("one message per stop, named with the reason and the clearing command", () => {
  it("sends exactly one Telegram message for a stop a run is held on", async () => {
    const result = await push("epic-2jn", heldDoc(), { i1r: "Follow ticfac from a phone" });
    expect(result.alerts.sent).toBe(1);
    expect(sentTexts()).toHaveLength(1);
    const text = sentTexts()[0]!;
    expect(text).toContain("epic-2jn");
    expect(text).toContain("needs a person");
    expect(text).toContain("attempt 13 of tick i1r is held for a person: blocked");
    // The clearing command, with its tick named the way a person reads it.
    expect(text).toContain("clear with: <code>ticfac settle 2jn i1r 13");
    expect(text).toContain("i1r (Follow ticfac from a phone)");
  });

  it("does not re-send the same stop on the next push — deduplicated per stop, not per poll", async () => {
    await push("epic-2jn", heldDoc());
    await ageAlertsPastRateLimit();
    const second = await push("epic-2jn", heldDoc());
    const third = await push("epic-2jn", heldDoc());
    expect(second.alerts.sent).toBe(0);
    expect(third.alerts.sent).toBe(0);
    expect(sentTexts()).toHaveLength(1);
  });

  it("re-arms when a stop clears and comes back — a returning stop is news", async () => {
    await push("epic-2jn", heldDoc());
    await ageAlertsPastRateLimit();
    // The run resumed: the hold is gone.
    const cleared = await push("epic-2jn", doc("epic-2jn", "2jn"));
    expect(cleared.alerts.cleared).toBe(1);
    // ...and it came back.
    const again = await push("epic-2jn", heldDoc());
    expect(again.alerts.sent).toBe(1);
    expect(sentTexts()).toHaveLength(2);
  });

  it("rate-limits a burst: the second distinct stop waits for the next evaluation", async () => {
    await push("epic-2jn", heldDoc());
    // A different reason is a different stop, but the rate limit holds it.
    const second = await push(
      "epic-2jn",
      heldDoc({
        attention: [
          {
            kind: "held-for-person",
            what: "attempt 14 of tick w9b is held for a person: blocked again",
            since: new Date().toISOString(),
            needs_person: true,
            unblock_command: 'ticfac settle 2jn w9b 14 --release "who"',
          },
        ],
      }),
    );
    expect(second.alerts.sent).toBe(0);
    expect(second.alerts.pending).toBe(1);
    expect(sentTexts()).toHaveLength(1);
    // Once the gap has passed, the same push delivers it.
    await ageAlertsPastRateLimit();
    const third = await push(
      "epic-2jn",
      heldDoc({
        attention: [
          {
            kind: "held-for-person",
            what: "attempt 14 of tick w9b is held for a person: blocked again",
            since: new Date().toISOString(),
            needs_person: true,
            unblock_command: 'ticfac settle 2jn w9b 14 --release "who"',
          },
        ],
      }),
    );
    expect(third.alerts.sent).toBe(1);
    expect(sentTexts()).toHaveLength(2);
  });

  it("tells the operator a run failed, with the resume as the clearing command", async () => {
    const result = await push(
      "epic-2jn",
      doc("epic-2jn", "2jn", {
        liveness: {
          alive: false,
          state: "dead",
          reason: "the gate refused the attempt",
          source: "run.pid",
        },
        lifecycle: { phase: "failed", phases: [], wave: null },
        waits_on: null,
      }),
    );
    expect(result.alerts.sent).toBe(1);
    const text = sentTexts()[0]!;
    expect(text).toContain("run failed");
    expect(text).toContain("the gate refused the attempt");
    expect(text).toContain("clear with: <code>ticfac run-epic 2jn</code>");
  });

  it("tells the operator an epic is done — once, and only when nothing holds", async () => {
    // A done run still holding the merge is the MERGE's message, not a
    // second "finished" one; here nothing holds.
    const first = await push(
      "epic-2jn",
      doc("epic-2jn", "2jn", {
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
    expect(first.alerts.sent).toBe(1);
    expect(sentTexts()[0]).toContain("epic done");

    const second = await push(
      "epic-2jn",
      doc("epic-2jn", "2jn", {
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
    expect(second.alerts.sent).toBe(0);
    expect(sentTexts()).toHaveLength(1);
  });
});

describe("a send the channel could not take stays pending and retries", () => {
  it("records the stop as pending when the Bot API refuses, and delivers it on the next push", async () => {
    const originalNow = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ ok: false, description: "not now" }), {
        status: 500,
      })) as typeof fetch;
    const refused = await push("epic-2jn", heldDoc());
    expect(refused.alerts.sent).toBe(0);
    expect(refused.alerts.pending).toBe(1);
    globalThis.fetch = originalNow;

    const retried = await push("epic-2jn", heldDoc());
    expect(retried.alerts.sent).toBe(1);
    expect(sentTexts()).toHaveLength(1);
  });
});

describe("the stop vocabulary (pure)", () => {
  it("keeps a stop's key stable across pushes of the same stop", () => {
    const a = stopsFromStatusDoc(heldDoc() as unknown as StatusDoc);
    const b = stopsFromStatusDoc(heldDoc() as unknown as StatusDoc);
    expect(a).toHaveLength(1);
    expect(a[0]!.key).toBe(b[0]!.key);
  });

  it("spells a failed run's resume by the host the run lives on (tick tt6)", () => {
    const failed = {
      liveness: {
        alive: false,
        state: "dead",
        reason: "the gate refused the attempt",
        source: "run.pid",
      },
      lifecycle: { phase: "failed", phases: [], wave: null },
      waits_on: null,
    };
    // A LOCAL run's resume is the foreground form the reconciler itself runs.
    const localStops = stopsFromStatusDoc(doc("epic-2jn", "2jn", failed) as unknown as StatusDoc);
    expect(localStops.find((stop) => stop.kind === "failed")!.clear_with).toBe(
      "ticfac run-epic 2jn",
    );
    // A CLOUD run's resume is a new submission to its factory: `run-epic` on
    // the reader's machine would restart the epic LOCALLY, in the foreground
    // — the same stop, a different run (the move ResumeCommand exists to
    // prevent).
    const cloudStops = stopsFromStatusDoc(
      doc("epic-2jn", "2jn", { ...failed, host: "cloud" }) as unknown as StatusDoc,
    );
    expect(cloudStops.find((stop) => stop.kind === "failed")!.clear_with).toBe(
      "ticfac run 2jn --cloud",
    );
  });

  it("answers a failed run's resume the same way the phone page does — one model, no disagreement (tick tt6)", () => {
    for (const host of ["local", "cloud"]) {
      const failed = doc("epic-2jn", "2jn", {
        liveness: {
          alive: false,
          state: "dead",
          reason: "the gate refused the attempt",
          source: "run.pid",
        },
        lifecycle: { phase: "failed", phases: [], wave: null },
        waits_on: null,
        host,
      }) as unknown as StatusDoc;
      const stop = stopsFromStatusDoc(failed).find((s) => s.kind === "failed")!;
      const page = classifyStatusDoc(failed);
      expect(page.clear_with).toBe(stop.clear_with);
    }
  });

  it("keeps a failed run's own resume one message and one class (tick jkb)", () => {
    // A failed run's model now carries its resume in attention, as the Go
    // model states it: the row keeps the failed class with the same resume
    // (never the held band), and the page is the one stop — never a second
    // "terminal:failed" message about the same move.
    const failed = doc("epic-2jn", "2jn", {
      liveness: {
        alive: false,
        state: "dead",
        reason: "the gate refused the attempt",
        source: "run.pid",
      },
      lifecycle: { phase: "failed", phases: [], wave: null },
      waits_on: {
        kind: "dead-run",
        what: "run epic-2jn failed: the gate refused the attempt",
        since: null,
        needs_person: true,
        unblock_command: "ticfac run-epic 2jn",
      },
      attention: [
        {
          kind: "dead-run",
          what: "run epic-2jn failed: the gate refused the attempt",
          since: null,
          needs_person: true,
          unblock_command: "ticfac run-epic 2jn",
        },
      ],
    }) as unknown as StatusDoc;
    const page = classifyStatusDoc(failed);
    expect(page.state).toBe("failed");
    expect(page.clear_with).toBe("ticfac run-epic 2jn");
    const stops = stopsFromStatusDoc(failed);
    expect(stops).toHaveLength(1);
    expect(stops[0]!.kind).toBe("person");
    expect(stops[0]!.clear_with).toBe("ticfac run-epic 2jn");
  });

  it("names no tick when the run pushed no labels", () => {
    const html = stopAlertHTML(
      "epic-2jn",
      "2jn",
      {
        key: "person:held-for-person:x",
        kind: "person",
        what: "held",
        clear_with: "ticfac settle 2jn w9b 9 --release who",
      },
      null,
    );
    expect(html).toContain("ticfac settle 2jn w9b 9");
    expect(html).not.toContain("w9b (");
  });

  it("evaluates a cloud run's ending through the same dedup memory", async () => {
    // notifyRunEnded routes here; the run-workflow finalization calls it.
    const { notifyRunEnded } = await import("../src/notify");
    await notifyRunEnded(
      env,
      { run_id: "run_cloud", project: "ticks-test/any", epic: "ko8" },
      "failed",
      "the orchestrator could not boot",
    );
    expect(sentTexts()).toHaveLength(1);
    expect(sentTexts()[0]).toContain("run_cloud");
    expect(sentTexts()[0]).toContain("run failed");
    // The resume the ending names is the cloud run's own: a new submission to
    // its factory (tick tt6), never the local foreground `run-epic`.
    expect(sentTexts()[0]).toContain("clear with: <code>ticfac run ko8 --cloud</code>");

    // A retried finalize step is the same ending: not a second page.
    await notifyRunEnded(
      env,
      { run_id: "run_cloud", project: "ticks-test/any", epic: "ko8" },
      "failed",
      "the orchestrator could not boot",
    );
    expect(sentTexts()).toHaveLength(1);
  });

  it("reports a needs-person stop that needs no send when attention is absent", async () => {
    const result = await evaluateStatusAlerts(env, {
      run_id: "epic-quiet",
      doc: doc("epic-quiet", "quiet") as unknown as StatusDoc,
    });
    expect(result).toEqual({ sent: 0, pending: 0, cleared: 0 });
  });
});
